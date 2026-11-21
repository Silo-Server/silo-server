package notifications

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// waitForLockWait waits until another backend is waiting on a row lock.
func waitForLockWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> pg_backend_pid())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-deadline:
			t.Fatal("no backend waited on a lock")
		case <-poll.C:
		}
	}
}

// A create and an account deletion lock the account first, so they wait on
// each other instead of deadlocking: the deletion cascades through the
// profile, which the create must not hold.
func TestPluginDestinationCreateAndAccountDeletionDontDeadlockDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	service, _ := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})

	deletion, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = deletion.Rollback(context.WithoutCancel(ctx)) }()
	// Account deletion locks the account row first (auth's admin delete).
	if _, err := deletion.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, accountA); err != nil {
		t.Fatal(err)
	}
	created := make(chan error, 1)
	go func() {
		name := "Phone"
		_, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
			Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
			Settings: map[string]any{"topic": "alerts"},
		})
		created <- err
	}()
	waitForLockWait(t, pool)
	if _, err := deletion.Exec(ctx, `DELETE FROM users WHERE id = $1`, accountA); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("account deletion deadlocked with a create: %v", err)
		}
		t.Fatal(err)
	}
	if err := deletion.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-created; !errors.Is(err, ErrPluginDestinationProfileGone) {
		t.Fatalf("create during the account deletion = %v, want ErrPluginDestinationProfileGone", err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_plugin_destinations`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d destinations left, %v; want none", left, err)
	}
}

// Profiles may live outside PostgreSQL, so a create checks the profile
// through the user store, again after it commits: a profile deleted between
// the two checks takes the new destination with it.
func TestPluginDestinationCreateRechecksTheProfileDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	service, _ := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})
	checks := 0
	service.profileExists = func(context.Context, int, string) (bool, error) {
		checks++
		return checks == 1, nil
	}
	name := "Phone"
	if _, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts"},
	}); !errors.Is(err, ErrPluginDestinationProfileGone) {
		t.Fatalf("create for a profile deleted meanwhile = %v, want ErrPluginDestinationProfileGone", err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_plugin_destinations`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d destinations left, %v; want none", left, err)
	}
}

// poolSettingReader reads every setting through the pool, as the server's
// settings repository does, and answers the default.
type poolSettingReader struct{ pool *pgxpool.Pool }

func (r poolSettingReader) Get(ctx context.Context, _ string) (string, error) {
	var value string
	err := r.pool.QueryRow(ctx, `SELECT ''`).Scan(&value)
	return value, err
}

// The auto-disable notice is written by the system's own writer in the
// transaction that disables the destination. With one pool connection, that
// writer must not need a second one, or the disable never commits.
func TestPluginDestinationAutoDisableOnAOneConnectionPoolDB(t *testing.T) {
	shared := pluginDestinationDB(t)
	cfg := shared.Config().Copy()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	system := &System{
		Settings:                NewSettings(poolSettingReader{pool}),
		Deliveries:              NewDeliveryRepository(pool),
		webhookRepo:             NewWebhookRepository(pool),
		webPushRepo:             NewWebPushRepository(pool),
		pushDeviceRepo:          NewPushDeviceRepository(pool),
		pluginDestinationSender: sender,
		dispatcher:              &dispatchRecorder{},
		pool:                    pool,
		logger:                  slog.New(slog.DiscardHandler),
	}
	system.wirePluginDestinationSender()
	// Only the notice path is under test; the message renders without the
	// system's links and posters.
	sender.posterURL, sender.linkBase, sender.payload = nil, nil, nil
	d := createTestDestination(t, service, map[string]any{"topic": "alerts"})

	deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
	enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
	claimed, err := sender.repo.ClaimPendingForDelivery(t.Context(), deliveryID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, Message: "revoked"})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sender.processAttempt(ctx, claimed[0])

	got, err := sender.repo.GetByID(t.Context(), accountA, sharedID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var notices int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_deliveries WHERE type = $1`, DeliveryTypePluginDestinationAutoDisabled).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if got.Enabled || notices != 1 {
		t.Fatalf("after DESTINATION_INVALID on a one-connection pool: enabled %v, %d notices; want disabled with a notice", got.Enabled, notices)
	}
}

// Reaching a plugin can take seconds. The destination is read again just
// before the send: an edit made meanwhile is what gets sent, and a
// destination turned off meanwhile gets nothing and counts no failure.
func TestPluginDestinationSendRereadsTheDestinationDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	d := createTestDestination(t, service, map[string]any{"topic": "alerts", "token": "old"})

	host.connecting = func() {
		if _, err := service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Settings: map[string]any{"token": "new"}}, anyRevision); err != nil {
			t.Errorf("edit while the plugin starts: %v", err)
		}
	}
	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY, Message: "busy"})
	processTestAttempt(t, pool, sender, d.ID, 1)
	if len(host.sent) != 1 || host.sent[0].GetConfig().AsMap()["token"] != "new" {
		t.Fatalf("sent %v, want the settings saved while the plugin started", host.sent)
	}
	// The failure belongs to the revision that was sent, the edited one.
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); got.ConsecutiveFailures != 1 {
		t.Fatalf("after a failed send of the edited settings: %d failures, want 1", got.ConsecutiveFailures)
	}

	off := false
	host.connecting = func() {
		if _, err := service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Enabled: &off}, anyRevision); err != nil {
			t.Errorf("turn off while the plugin starts: %v", err)
		}
	}
	attempt := processTestAttempt(t, pool, sender, d.ID, 1)
	if len(host.sent) != 1 || attempt.Outcome != WebhookOutcomeFailed {
		t.Fatalf("after turning it off mid-start: %d sends, attempt %s; want no send and the attempt closed", len(host.sent), attempt.Outcome)
	}
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); got.ConsecutiveFailures != 1 || got.Enabled {
		t.Fatalf("after turning it off mid-start: %+v, want it off with no failure added", got)
	}

	// A test message reads the destination again too.
	host.connecting = func() {
		if _, err := service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Settings: map[string]any{"token": "newer"}}, anyRevision); err != nil {
			t.Errorf("edit while a test starts: %v", err)
		}
	}
	if _, err := service.Test(ctx, accountA, sharedID, d.ID); err != nil {
		t.Fatal(err)
	}
	if len(host.sent) != 2 || host.sent[1].GetConfig().AsMap()["token"] != "newer" {
		t.Fatalf("test sent %v, want the settings saved while the plugin started", host.sent)
	}
	host.connecting = func() {
		if err := service.DeleteGuarded(ctx, accountA, sharedID, d.ID, anyRevision); err != nil {
			t.Errorf("delete while a test starts: %v", err)
		}
	}
	if _, err := service.Test(ctx, accountA, sharedID, d.ID); !errors.Is(err, ErrPluginDestinationNotFound) || len(host.sent) != 2 {
		t.Fatalf("test of a destination deleted mid-start = %v after %d sends, want ErrPluginDestinationNotFound and no send", err, len(host.sent))
	}
}

// A plugin message PostgreSQL can't store as text would fail the finalize,
// leave the attempt number where it was, and resend the message forever.
func TestPluginDestinationNULMessageIsStoredDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	d := createTestDestination(t, service, map[string]any{"topic": "alerts"})

	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY, Message: "bad\x00 gate\xffway"})
	attempt := processTestAttempt(t, pool, sender, d.ID, 1)
	if attempt.AttemptNumber != 1 || attempt.FailureMessage == nil || *attempt.FailureMessage != "bad gateway" {
		t.Fatalf("attempt %+v, want it finalized as attempt 1 with the cleaned message", attempt)
	}
	requireRetryingAfter(t, attempt, 0)
}

// An attempt whose delivery is older than the retry window and its margin
// finishes without a send, whatever its attempt count. It disables only as
// an exhausted schedule would: after an ordinary failure, never after one
// the destination isn't at fault for or before any send.
func TestPluginDestinationAttemptAgeCapDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	d := createTestDestination(t, service, map[string]any{"topic": "alerts"})

	deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
	enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
	if _, err := pool.Exec(ctx, `UPDATE notification_deliveries SET created_at = now() - $2::interval WHERE id = $1`,
		deliveryID, pluginAttemptMaxAge+time.Minute); err != nil {
		t.Fatal(err)
	}
	claimed, err := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	sender.processAttempt(ctx, claimed[0])
	var outcome string
	if err := pool.QueryRow(ctx, `SELECT outcome FROM plugin_destination_delivery_attempts WHERE id = $1`, claimed[0].ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID)
	if outcome != WebhookOutcomeFailed || len(host.sent) != 0 || !got.Enabled || got.ConsecutiveFailures != 0 {
		t.Fatalf("aged attempt: outcome %s, %d sends, destination %+v; want failed, unsent, the destination untouched", outcome, len(host.sent), got)
	}

	for _, tc := range []struct {
		lastFailure string
		disable     bool
	}{
		{lastFailure: pluginUnavailableText},
		{lastFailure: pluginSettingsUnreadableText},
		{lastFailure: "rate limited", disable: true},
		// A plugin's bare RETRY stores no message; it is an ordinary failure.
		{lastFailure: "", disable: true},
	} {
		deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
		enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
		// The destination was edited when it was created, before this
		// delivery is backdated past it.
		if _, err := pool.Exec(ctx, `UPDATE notification_deliveries SET created_at = now() - $2::interval WHERE id = $1`,
			deliveryID, pluginAttemptMaxAge+time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE notification_plugin_destinations SET enabled = true, disabled_reason = NULL, updated_at = now() - $2::interval WHERE id = $1`,
			d.ID, pluginAttemptMaxAge+time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE plugin_destination_delivery_attempts SET attempt_number = 3, failure_message = NULLIF($2, '') WHERE notification_delivery_id = $1`,
			deliveryID, tc.lastFailure); err != nil {
			t.Fatal(err)
		}
		claimed, err := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("claim = %d, %v", len(claimed), err)
		}
		sender.processAttempt(ctx, claimed[0])
		got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID)
		if got.Enabled == tc.disable || len(host.sent) != 0 {
			t.Fatalf("aged attempt after %q: enabled %v, %d sends; want disabled %v and no send", tc.lastFailure, got.Enabled, len(host.sent), tc.disable)
		}
	}
}

// One delivery transaction enqueues for a profile's destinations in several
// calls, so it can lock them in any order. A profile purge takes the
// profile's inbox lock first and waits for the whole transaction instead of
// deadlocking with it, and then leaves nothing of the profile behind.
func TestPluginDestinationPurgeWaitsForADeliveryDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	service, sender := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})
	var ids []string
	for _, name := range []string{"Phone", "Tablet"} {
		d, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
			Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
			Settings: map[string]any{"topic": "alerts"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	slices.Sort(ids)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	inserted, err := NewDeliveryRepository(pool).BulkInsert(ctx, tx, []Delivery{{
		ID: "delivery-purge", UserID: accountA, ProfileID: sharedID,
		Type: DeliveryTypeRequestApproved, ReasonFlags: []byte(`{"title":"Heat"}`),
	}})
	if err != nil || len(inserted) != 1 {
		t.Fatalf("insert delivery: %v, %d rows", err, len(inserted))
	}
	enqueue := func(target string) {
		t.Helper()
		if _, err := sender.repo.EnqueueAttempts(ctx, tx, []DeliveryAttempt{{ID: "attempt-" + target, NotificationDeliveryID: "delivery-purge", TargetID: target}}); err != nil {
			t.Fatal(err)
		}
	}
	// The later destination first, then the purge, then the earlier one: the
	// order that deadlocked when the purge locked only the destinations.
	enqueue(ids[1])
	purged := make(chan error, 1)
	go func() { purged <- sender.repo.DeleteAllForProfile(ctx, accountA, sharedID) }()
	waitForLockWait(t, pool)
	enqueue(ids[0])
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-purged; err != nil {
		t.Fatalf("purge alongside a delivery: %v", err)
	}
	var destinations, attempts int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM notification_plugin_destinations),
		(SELECT count(*) FROM plugin_destination_delivery_attempts)`).Scan(&destinations, &attempts); err != nil {
		t.Fatal(err)
	}
	if destinations != 0 || attempts != 0 {
		t.Fatalf("after the purge: %d destinations, %d attempts; want none", destinations, attempts)
	}
}

// A profile at its limit answers a taken name as a taken name, the error
// the owner can fix in place.
func TestPluginDestinationTakenNameAtTheLimitDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	_, sender := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})
	d := PluginDestination{ID: "first", UserID: accountA, ProfileID: sharedID, PluginID: "example.ntfy",
		CapabilityID: "ntfy", Name: "Phone", ConfigCiphertext: "x", Enabled: true}
	if err := sender.repo.InsertWithLimit(ctx, d, 1); err != nil {
		t.Fatal(err)
	}
	d.ID = "second"
	if err := sender.repo.InsertWithLimit(ctx, d, 1); !errors.Is(err, ErrPluginDestinationNameTaken) {
		t.Fatalf("taken name at the limit = %v, want ErrPluginDestinationNameTaken", err)
	}
	d.Name = "Tablet"
	if err := sender.repo.InsertWithLimit(ctx, d, 1); !errors.Is(err, ErrPluginDestinationLimit) {
		t.Fatalf("new name at the limit = %v, want ErrPluginDestinationLimit", err)
	}
}

// An auto-disable writes a notice whose foreign key key-share locks the
// account, so it takes that lock before the destination's, the order an
// account deletion takes them. Otherwise the two deadlock and PostgreSQL
// aborts the disable.
func TestPluginDestinationAutoDisableAndAccountDeletionDontDeadlockDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	// The inbox row goes with its account, as in production.
	if _, err := pool.Exec(ctx, `ALTER TABLE notification_deliveries ADD FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE`); err != nil {
		t.Fatal(err)
	}
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	recordNotices(pool, sender)
	var logs strings.Builder
	var logMu sync.Mutex
	sender.logger = slog.New(slog.NewTextHandler(lockedWriter{&logMu, &logs}, nil))
	d := createTestDestination(t, service, map[string]any{"topic": "alerts"})
	deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
	enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
	claimed, err := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, Message: "revoked"})

	deletion, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = deletion.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := deletion.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, accountA); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sender.processAttempt(ctx, claimed[0])
	}()
	waitForLockWait(t, pool)
	if _, err := deletion.Exec(ctx, `DELETE FROM users WHERE id = $1`, accountA); err != nil {
		t.Fatalf("account deletion alongside an auto-disable: %v", err)
	}
	if err := deletion.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	logMu.Lock()
	defer logMu.Unlock()
	if strings.Contains(logs.String(), "40P01") {
		t.Fatalf("the auto-disable deadlocked with the account deletion: %s", logs.String())
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_plugin_destinations`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d destinations left, %v; want none", left, err)
	}
}

// A profile purge holds the profile's inbox lock before it locks the
// profile's destinations. An auto-disable writes a notice into that inbox, so
// it takes the inbox lock before the destination too and waits for the purge
// instead of deadlocking with it.
func TestPluginDestinationAutoDisableAndProfilePurgeDontDeadlockDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	recordNotices(pool, sender)
	var logs strings.Builder
	var logMu sync.Mutex
	sender.logger = slog.New(slog.NewTextHandler(lockedWriter{&logMu, &logs}, nil))
	d := createTestDestination(t, service, map[string]any{"topic": "alerts"})
	deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
	enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
	claimed, err := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, Message: "revoked"})

	purge, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = purge.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockInboxProfiles(ctx, purge, []string{sharedID}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sender.processAttempt(ctx, claimed[0])
	}()
	waitForLockWait(t, pool)
	if _, err := purge.Exec(ctx, `SELECT 1 FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2 ORDER BY id FOR UPDATE`, accountA, sharedID); err != nil {
		t.Fatalf("purge lock: %v", err)
	}
	if _, err := purge.Exec(ctx, `DELETE FROM notification_plugin_destinations WHERE user_id = $1 AND profile_id = $2`, accountA, sharedID); err != nil {
		t.Fatalf("purge delete: %v", err)
	}
	if err := purge.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	logMu.Lock()
	defer logMu.Unlock()
	if strings.Contains(logs.String(), "40P01") {
		t.Fatalf("the auto-disable deadlocked with the purge: %s", logs.String())
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_plugin_destinations`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d destinations left, %v; want none", left, err)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

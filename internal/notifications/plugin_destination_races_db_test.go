package notifications

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// waitForLockWait waits until another backend is waiting on a row lock.
func waitForLockWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> pg_backend_pid())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no backend waited on a lock")
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
// finishes as failed without a send and without disabling, whatever its
// attempt count.
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
}

package notifications

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// scriptedChannelHost is a plugin that answers sends from a script.
type scriptedChannelHost struct {
	mu       sync.Mutex
	outcomes []*pluginv1.SendNotificationResponse
	sent     []*pluginv1.SendNotificationRequest
	// during runs inside the next send, before it answers.
	during func()
	// validating runs inside the next settings check, before it answers.
	validating func()
	// unavailable makes sends fail as if the plugin were turned off.
	unavailable bool
	// connecting runs inside the next Connect, before it answers.
	connecting func()
	// reject makes settings checks refuse with this message.
	reject string
	// sendErr makes the next send fail with this error instead of answering.
	sendErr error
}

func (h *scriptedChannelHost) AvailableChannels(ctx context.Context, _ pgx.Tx) ([]PluginChannelRef, error) {
	channels, err := h.Channels(ctx)
	refs := make([]PluginChannelRef, 0, len(channels))
	for _, channel := range channels {
		refs = append(refs, channel.PluginChannelRef)
	}
	return refs, err
}

func (h *scriptedChannelHost) Channels(context.Context) ([]PluginChannel, error) {
	return []PluginChannel{{
		PluginChannelRef: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"},
		DisplayName:      "ntfy",
		Schema: &pluginv1.ConfigSchema{
			JsonSchema: `{"type":"object","required":["topic"],"properties":{"topic":{"type":"string","maxLength":32},"token":{"type":"string"},"priority":{"type":"integer","minimum":1,"maximum":5},"sound":{"type":"string","enum":["bell","none"]}}}`,
			AdminForm:  &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{Key: "topic"}, {Key: "token", Secret: true}, {Key: "priority"}, {Key: "sound"}}},
		},
		PublicFields: []string{"topic", "priority", "sound"},
		SecretFields: []string{"token"},
	}}, nil
}

func (h *scriptedChannelHost) Connect(ctx context.Context, ref PluginChannelRef) (*PluginChannel, PluginChannelClient, error) {
	h.mu.Lock()
	unavailable := h.unavailable
	connecting := h.connecting
	h.connecting = nil
	h.mu.Unlock()
	if connecting != nil {
		connecting()
	}
	if unavailable {
		return nil, nil, ErrPluginChannelUnavailable
	}
	channels, _ := h.Channels(ctx)
	for i := range channels {
		if channels[i].PluginChannelRef == ref {
			return &channels[i], h, nil
		}
	}
	return nil, nil, ErrPluginChannelUnavailable
}

func (h *scriptedChannelHost) ValidateDestination(context.Context, *pluginv1.ValidateNotificationDestinationRequest) (*pluginv1.ValidateNotificationDestinationResponse, error) {
	h.mu.Lock()
	validating := h.validating
	h.validating = nil
	reject := h.reject
	h.mu.Unlock()
	if validating != nil {
		validating()
	}
	if reject != "" {
		return &pluginv1.ValidateNotificationDestinationResponse{Message: reject}, nil
	}
	return &pluginv1.ValidateNotificationDestinationResponse{Valid: true}, nil
}

func (h *scriptedChannelHost) SendNotification(_ context.Context, req *pluginv1.SendNotificationRequest) (*pluginv1.SendNotificationResponse, error) {
	h.mu.Lock()
	during := h.during
	h.during = nil
	h.mu.Unlock()
	if during != nil {
		during()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, req)
	if err := h.sendErr; err != nil {
		h.sendErr = nil
		return nil, err
	}
	if len(h.outcomes) == 0 {
		return &pluginv1.SendNotificationResponse{}, nil
	}
	next := h.outcomes[0]
	h.outcomes = h.outcomes[1:]
	return next, nil
}

// pluginDestinationDB is a schema with the delivery inbox and the plugin
// destination tables, built from their migration.
func pluginDestinationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := inboxPageDB(t)
	data, err := os.ReadFile("../../migrations/sql/20261104202334_plugin_notification_destinations.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(strings.ReplaceAll(string(data), "public.", ""), "-- +goose Down")
	if _, err := pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	// Destinations are created only for accounts and profiles that exist,
	// and go with their account, as in production.
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE users (LIKE public.users INCLUDING DEFAULTS);
		ALTER TABLE users ADD PRIMARY KEY (id);
		CREATE TABLE user_profiles (LIKE public.user_profiles INCLUDING DEFAULTS);
		ALTER TABLE user_profiles ADD PRIMARY KEY (user_id, id),
			ADD FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
		ALTER TABLE notification_plugin_destinations ADD FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, role, username) VALUES ($1, 'user', 'a'), ($2, 'user', 'b')`, accountA, accountB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'A'), ($1, $3, 'B')`,
		sharedID, accountA, accountB); err != nil {
		t.Fatal(err)
	}
	return pool
}

func newTestPluginDestinationService(t *testing.T, pool *pgxpool.Pool, host PluginChannelHost) (*PluginDestinationService, *pluginDestinationSender) {
	t.Helper()
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	sender := newPluginDestinationSender(NewPluginDestinationRepository(pool), NewDeliveryRepository(pool), cipher)
	sender.logger = slog.New(slog.DiscardHandler)
	sender.hostBox.Store(&pluginChannelHostBox{host: host})
	service := newPluginDestinationService(sender.repo, sender)
	service.profileExists = func(ctx context.Context, userID int, profileID string) (bool, error) {
		var exists bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_profiles WHERE user_id = $1 AND id = $2)`, userID, profileID).Scan(&exists)
		return exists, err
	}
	return service, sender
}

func insertTestDelivery(t *testing.T, pool *pgxpool.Pool, userID int, profileID string) string {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	inserted, err := NewDeliveryRepository(pool).BulkInsert(t.Context(), tx, []Delivery{{
		ID: "delivery-" + profileID + "-" + time.Now().Format("150405.000000000"), UserID: userID, ProfileID: profileID,
		Type: DeliveryTypeRequestApproved, ReasonFlags: []byte(`{"title":"Heat"}`),
	}})
	if err != nil || len(inserted) != 1 {
		t.Fatalf("insert delivery: %v, %d rows", err, len(inserted))
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return inserted[0].ID
}

func enqueueTestAttempt(t *testing.T, pool *pgxpool.Pool, repo *PluginDestinationRepository, deliveryID, destinationID string) {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := repo.EnqueueAttempts(t.Context(), tx, []DeliveryAttempt{{ID: "attempt-" + deliveryID, NotificationDeliveryID: deliveryID, TargetID: destinationID}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func anyRevision(int64) error { return nil }

// recordNotices has the sender write its auto-disable notices to the inbox,
// in its own transaction as the system does, and returns them as committed.
func recordNotices(pool *pgxpool.Pool, sender *pluginDestinationSender) *[]Delivery {
	var notices []Delivery
	sender.noticeWriter = func(context.Context) func(context.Context, pgx.Tx, Delivery) (*InsertedDelivery, error) {
		return func(ctx context.Context, tx pgx.Tx, notice Delivery) (*InsertedDelivery, error) {
			inserted, err := NewDeliveryRepository(pool).BulkInsert(ctx, tx, []Delivery{notice})
			if err != nil || len(inserted) == 0 {
				return nil, err
			}
			return &inserted[0], nil
		}
	}
	sender.dispatchNotice = func(_ context.Context, row InsertedDelivery) {
		notices = append(notices, Delivery{ID: row.ID, UserID: row.UserID, ProfileID: row.ProfileID, Type: DeliveryTypePluginDestinationAutoDisabled})
	}
	return &notices
}

// processTestAttempt queues one of account A's deliveries to the
// destination as its attemptNumber-th try, claims it, and processes it.
func processTestAttempt(t *testing.T, pool *pgxpool.Pool, sender *pluginDestinationSender, destinationID string, attemptNumber int) DeliveryAttempt {
	t.Helper()
	ctx := t.Context()
	deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
	enqueueTestAttempt(t, pool, sender.repo, deliveryID, destinationID)
	if _, err := pool.Exec(ctx, `UPDATE plugin_destination_delivery_attempts SET attempt_number = $2 WHERE notification_delivery_id = $1`,
		deliveryID, attemptNumber-1); err != nil {
		t.Fatal(err)
	}
	claimed, err := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d attempts, %v", len(claimed), err)
	}
	// A second claim finds nothing: the lease hides the attempt.
	if again, _ := sender.repo.ClaimPendingForDelivery(ctx, deliveryID); len(again) != 0 {
		t.Fatalf("claimed the same attempt twice")
	}
	sender.processAttempt(ctx, claimed[0])
	var attempt DeliveryAttempt
	if err := pool.QueryRow(ctx, `SELECT `+pluginDestinationAttemptColumns+` FROM plugin_destination_delivery_attempts WHERE id = $1`, claimed[0].ID).
		Scan(&attempt.ID, &attempt.NotificationDeliveryID, &attempt.TargetID, &attempt.AttemptNumber, &attempt.AttemptedAt,
			&attempt.NextRetryAt, &attempt.Outcome, &attempt.FailureMessage); err != nil {
		t.Fatal(err)
	}
	return attempt
}

// requireRetryingAfter fails unless the attempt waits at least atLeast for
// its next try.
func requireRetryingAfter(t *testing.T, attempt DeliveryAttempt, atLeast time.Duration) {
	t.Helper()
	if attempt.Outcome != WebhookOutcomeRetrying || attempt.NextRetryAt == nil || time.Until(*attempt.NextRetryAt) < atLeast {
		t.Fatalf("attempt %+v, want retrying in at least %s", attempt, atLeast)
	}
}

// Profile IDs repeat across accounts: account B's "default" profile must
// never read, change or delete account A's destination.
func TestPluginDestinationsStayOnTheirAccountDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	service, _ := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})
	name := "Phone"
	created, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts", "token": "s3cret"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := service.repo.GetByID(ctx, accountB, sharedID, created.ID); !errors.Is(err, ErrPluginDestinationNotFound) {
		t.Fatalf("other account GetByID = %v, want not found", err)
	}
	if rows, err := service.ListPage(ctx, accountB, sharedID, 10, nil); err != nil || len(rows) != 0 {
		t.Fatalf("other account ListPage = %d rows, %v", len(rows), err)
	}
	disabled := false
	if _, err := service.UpdateGuarded(ctx, accountB, sharedID, created.ID, PluginDestinationInput{Enabled: &disabled}, anyRevision); !errors.Is(err, ErrPluginDestinationNotFound) {
		t.Fatalf("other account UpdateGuarded = %v, want not found", err)
	}
	if err := service.DeleteGuarded(ctx, accountB, sharedID, created.ID, anyRevision); !errors.Is(err, ErrPluginDestinationNotFound) {
		t.Fatalf("other account DeleteGuarded = %v, want not found", err)
	}

	// A profile deleted meanwhile gets no destination.
	if _, err := service.Create(ctx, accountA, "deleted-profile", PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts"},
	}); !errors.Is(err, ErrPluginDestinationProfileGone) {
		t.Fatalf("create for a missing profile = %v, want ErrPluginDestinationProfileGone", err)
	}

	// The owner sees it, without the secret.
	rows, err := service.ListPage(ctx, accountA, sharedID, 10, nil)
	if err != nil || len(rows) != 1 || !rows[0].Enabled {
		t.Fatalf("owner ListPage = %+v, %v", rows, err)
	}
	if _, leaked := rows[0].Settings["token"]; leaked || rows[0].Settings["topic"] != "alerts" || len(rows[0].SecretsSet) != 1 {
		t.Fatalf("owner settings = %v, secrets set %v", rows[0].Settings, rows[0].SecretsSet)
	}
}

// One attempt through the outbox: a temporary failure is retried on the
// schedule, waiting at least as long as the plugin asks, a send to a broken destination disables
// it and posts a notice, and a delivery resets the failure streak.
func TestPluginDestinationDeliveryOutcomesDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	notices := recordNotices(pool, sender)
	name := "Phone"
	d, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts"},
	})
	if err != nil {
		t.Fatal(err)
	}

	send := func(outcome *pluginv1.SendNotificationResponse) DeliveryAttempt {
		t.Helper()
		host.outcomes = append(host.outcomes, outcome)
		return processTestAttempt(t, pool, sender, d.ID, 1)
	}

	requireRetryingAfter(t, send(&pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY, RetryAfterSeconds: 600, Message: "rate limited"}), 9*time.Minute)
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); got.ConsecutiveFailures != 1 || got.LastFailureMessage == nil || *got.LastFailureMessage != "rate limited" {
		t.Fatalf("after RETRY the destination is %+v", got)
	}
	// retry_after_seconds is a minimum: a short wait late in the schedule
	// doesn't pull the next attempt in from the schedule's 6 hours.
	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY, RetryAfterSeconds: 60})
	requireRetryingAfter(t, processTestAttempt(t, pool, sender, d.ID, 8), 5*time.Hour)

	delivered := send(&pluginv1.SendNotificationResponse{})
	if delivered.Outcome != WebhookOutcomeDelivered {
		t.Fatalf("after an empty response the attempt is %s, want delivered", delivered.Outcome)
	}
	// Delivery bookkeeping leaves the revision, so an owner's edit begun
	// before these sends isn't refused as stale.
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); got.ConsecutiveFailures != 0 || got.LastSuccessAt == nil || got.Revision != d.Revision {
		t.Fatalf("after delivery the destination is %+v, want the streak reset and revision %d", got, d.Revision)
	}

	// A plugin that stays off through the whole retry schedule drops the
	// message but leaves the destination enabled for when it's back.
	host.unavailable = true
	unavailable := processTestAttempt(t, pool, sender, d.ID, webhookMaxAttempts)
	host.unavailable = false
	if unavailable.Outcome != WebhookOutcomeFailed {
		t.Fatalf("after the schedule ran out with the plugin off the attempt is %s, want failed", unavailable.Outcome)
	}
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); !got.Enabled || got.DisabledReason != nil || len(*notices) != 0 {
		t.Fatalf("after the plugin was off the destination is %+v with %d notices, want it enabled", got, len(*notices))
	}

	invalid := send(&pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, Message: "token revoked"})
	if invalid.Outcome != WebhookOutcomeFailed {
		t.Fatalf("after DESTINATION_INVALID the attempt is %s, want failed", invalid.Outcome)
	}
	got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID)
	if got.Enabled || got.DisabledReason == nil || !strings.Contains(*got.DisabledReason, "token revoked") {
		t.Fatalf("after DESTINATION_INVALID the destination is %+v, want disabled with the reason", got)
	}
	if len(*notices) != 1 || (*notices)[0].Type != DeliveryTypePluginDestinationAutoDisabled || (*notices)[0].UserID != accountA {
		t.Fatalf("notices = %+v, want one auto-disable notice to the owner", *notices)
	}
	if req := host.sent[len(host.sent)-1]; req.GetDeliveryKey() == "" || req.GetConfig().AsMap()["topic"] != "alerts" || req.GetCapabilityId() != "ntfy" {
		t.Fatalf("send request = %v, want the delivery key, settings and capability", req)
	}

	// A disabled destination's queued attempts close without a send.
	sends := len(host.sent)
	deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
	enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
	claimed, _ := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
	sender.processAttempt(ctx, claimed[0])
	if len(host.sent) != sends {
		t.Fatal("sent to a disabled destination")
	}
}

// When a message's retry schedule runs out, the destination is disabled
// only if nothing reached it and its owner didn't edit it since the message
// was queued, and only when the plugin answered: one stuck message, or a
// plugin process that died mid-call, doesn't turn off a working destination.
func TestPluginDestinationExhaustedScheduleDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	notices := recordNotices(pool, sender)
	name := "Phone"
	d, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	retry := &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY, Message: "busy"}
	queue := func() string {
		t.Helper()
		deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
		enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
		return deliveryID
	}
	// lastTry sends a queued message's final attempt.
	lastTry := func(deliveryID string) string {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE plugin_destination_delivery_attempts SET attempt_number = $2 WHERE notification_delivery_id = $1`,
			deliveryID, webhookMaxAttempts-1); err != nil {
			t.Fatal(err)
		}
		claimed, err := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("claim = %d attempts, %v", len(claimed), err)
		}
		sender.processAttempt(ctx, claimed[0])
		var outcome string
		if err := pool.QueryRow(ctx, `SELECT outcome FROM plugin_destination_delivery_attempts WHERE id = $1`, claimed[0].ID).Scan(&outcome); err != nil {
			t.Fatal(err)
		}
		return outcome
	}
	requireEnabled := func(step string) {
		t.Helper()
		if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); !got.Enabled || len(*notices) != 0 {
			t.Fatalf("%s: destination %+v with %d notices, want it enabled", step, got, len(*notices))
		}
	}

	// Another message got through after this one was queued.
	stuck := queue()
	if delivered := processTestAttempt(t, pool, sender, d.ID, 1); delivered.Outcome != WebhookOutcomeDelivered {
		t.Fatalf("other message = %s, want delivered", delivered.Outcome)
	}
	host.outcomes = append(host.outcomes, retry)
	if outcome := lastTry(stuck); outcome != WebhookOutcomeFailed {
		t.Fatalf("stuck message after a later success = %s, want failed", outcome)
	}
	requireEnabled("after a later success")

	// The owner fixed the settings after the message was queued.
	queued := queue()
	topic := "fixed"
	if _, err := service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Settings: map[string]any{"topic": topic}}, anyRevision); err != nil {
		t.Fatal(err)
	}
	if d, err = sender.repo.GetByID(ctx, accountA, sharedID, d.ID); err != nil {
		t.Fatal(err)
	}
	host.outcomes = append(host.outcomes, retry)
	if outcome := lastTry(queued); outcome != WebhookOutcomeFailed {
		t.Fatalf("message queued before an edit = %s, want failed", outcome)
	}
	requireEnabled("after an edit")

	// The plugin process exited during the last call.
	host.sendErr = status.Error(codes.Unavailable, "error reading from server: EOF")
	if outcome := lastTry(queue()); outcome != WebhookOutcomeFailed {
		t.Fatalf("message whose plugin died = %s, want failed", outcome)
	}
	requireEnabled("after the plugin died")

	// Nothing got through since the message was queued: disabled, with a notice.
	host.outcomes = append(host.outcomes, retry)
	if outcome := lastTry(queue()); outcome != WebhookOutcomeAutoDisabled {
		t.Fatalf("message on a failing destination = %s, want auto_disabled", outcome)
	}
	got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID)
	if got.Enabled || got.DisabledReason == nil || len(*notices) != 1 {
		t.Fatalf("failing destination is %+v with %d notices, want disabled with one notice", got, len(*notices))
	}
}

// A send can outlive its lease, or race its owner's edit. Neither may let
// the stale send win: a lease another server took over keeps that server's
// result, and settings repaired during a failing send stay enabled.
func TestPluginDestinationStaleSendsDontWinDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	notices := recordNotices(pool, sender)
	name := "Phone"
	d, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts", "token": "old"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Lease lost: this server's claim expires and another server claims the
	// attempt again before this one finishes.
	deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
	enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
	stale, err := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil || len(stale) != 1 {
		t.Fatalf("claim = %d, %v", len(stale), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE plugin_destination_delivery_attempts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, stale[0].ID); err != nil {
		t.Fatal(err)
	}
	if fresh, err := sender.repo.ClaimDue(ctx, 10); err != nil || len(fresh) != 1 {
		t.Fatalf("second claim = %d, %v", len(fresh), err)
	}
	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID})
	sends := len(host.sent)
	sender.processAttempt(ctx, stale[0])
	if len(host.sent) != sends {
		t.Fatal("sent after the claim was lost: the newer claim's server sends it")
	}
	var outcome string
	if err := pool.QueryRow(ctx, `SELECT outcome FROM plugin_destination_delivery_attempts WHERE id = $1`, stale[0].ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); outcome != WebhookOutcomeRetrying || !got.Enabled || got.ConsecutiveFailures != 0 {
		t.Fatalf("after a stale send: attempt %s, destination %+v; want the newer claim untouched and the destination as it was", outcome, got)
	}

	// Settings repaired mid-send: the owner saves a working token while a
	// send with the revoked one is in flight, and that send then fails.
	deliveryID = insertTestDelivery(t, pool, accountA, sharedID)
	enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
	claimed, err := sender.repo.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	host.during = func() {
		if _, err := service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Settings: map[string]any{"token": "new"}}, anyRevision); err != nil {
			t.Errorf("update during send: %v", err)
		}
	}
	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, Message: "token revoked"})
	sender.processAttempt(ctx, claimed[0])
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); !got.Enabled || got.DisabledReason != nil || got.ConsecutiveFailures != 0 || len(*notices) != 0 {
		t.Fatalf("after a stale failure: destination %+v, %d notices; want the repaired destination enabled, with no failure counted and no notice", got, len(*notices))
	}
}

// A plugin can take seconds to check new settings, so it checks them without
// the row lock. A write that lands meanwhile stands, and the checked settings
// are refused as a stale If-Match would be, with nothing written.
func TestPluginDestinationValidationHoldsNoRowLockDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	name := "Phone"
	d, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts", "token": "old"},
	})
	if err != nil {
		t.Fatal(err)
	}

	renamed := "Laptop"
	host.validating = func() {
		// Under a row lock this rename would wait out the whole check.
		renameCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if _, err := service.UpdateGuarded(renameCtx, accountA, sharedID, d.ID, PluginDestinationInput{Name: &renamed}, anyRevision); err != nil {
			t.Errorf("rename during the settings check: %v", err)
		}
	}
	_, err = service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Settings: map[string]any{"topic": "news"}}, anyRevision)
	conflict, ok := errors.AsType[*PluginDestinationConflictError](err)
	if !ok {
		t.Fatalf("settings update after a concurrent rename = %v, want PluginDestinationConflictError", err)
	}
	got, err := service.repo.GetByID(ctx, accountA, sharedID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := sender.decryptConfig(got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != renamed || conflict.Revision != got.Revision || stored.Values["topic"] != "alerts" {
		t.Fatalf("after the conflict: name %q, revision %d (conflict names %d), topic %v; want the rename kept and the settings unchanged",
			got.Name, got.Revision, conflict.Revision, stored.Values["topic"])
	}
}

// A destination whose plugin is off or removed gets nothing queued; it still
// lists, as unavailable.
func TestPluginDestinationsOfAMissingPluginGetNothingQueuedDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	service, sender := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})
	name := "Phone"
	working, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orphan := PluginDestination{ID: "orphan", UserID: accountA, ProfileID: sharedID, PluginID: "example.removed",
		CapabilityID: "pushover", Name: "Old", ConfigCiphertext: working.ConfigCiphertext, Enabled: true}
	if err := sender.repo.InsertWithLimit(ctx, orphan, PluginDestinationMaxPerProfile); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	owner := profileKey{accountA, sharedID}
	byProfile, err := sender.enqueueable(ctx, tx, []profileKey{owner})
	if err != nil || len(byProfile[owner]) != 1 || byProfile[owner][0].ID != working.ID {
		t.Fatalf("enqueueable = %+v, %v; want only the destination whose plugin is enabled", byProfile[owner], err)
	}
	rows, err := service.ListPage(ctx, accountA, sharedID, 10, nil)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListPage = %d rows, %v", len(rows), err)
	}
	for _, row := range rows {
		if (row.Channel == nil) != (row.ID == orphan.ID) {
			t.Fatalf("destination %s channel = %v, want only the removed plugin's unavailable", row.ID, row.Channel)
		}
	}
}

func createTestDestination(t *testing.T, service *PluginDestinationService, settings map[string]any) *PluginDestination {
	t.Helper()
	name := "Phone"
	d, err := service.Create(t.Context(), accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A destination deleted after the delivery transaction listed it must not
// abort that transaction on the attempt's foreign key: the inbox row and
// every other channel's attempts would go with it.
func TestPluginDestinationEnqueueSurvivesADeleteDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	service, sender := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})
	d := createTestDestination(t, service, map[string]any{"topic": "alerts"})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	inserted, err := NewDeliveryRepository(pool).BulkInsert(ctx, tx, []Delivery{{
		ID: "delivery-race", UserID: accountA, ProfileID: sharedID, Type: DeliveryTypeRequestApproved, ReasonFlags: []byte(`{}`),
	}})
	if err != nil || len(inserted) != 1 {
		t.Fatalf("insert delivery: %v", err)
	}
	owner := profileKey{accountA, sharedID}
	byProfile, err := sender.enqueueable(ctx, tx, []profileKey{owner})
	if err != nil || len(byProfile[owner]) != 1 {
		t.Fatalf("enqueueable = %+v, %v", byProfile, err)
	}
	// The owner deletes it before the attempt is inserted.
	if err := service.DeleteGuarded(ctx, accountA, sharedID, d.ID, anyRevision); err != nil {
		t.Fatal(err)
	}
	queued, err := sender.repo.EnqueueAttempts(ctx, tx, []DeliveryAttempt{{ID: "attempt-race", NotificationDeliveryID: inserted[0].ID, TargetID: d.ID}})
	if err != nil {
		t.Fatalf("enqueue after the delete: %v", err)
	}
	// The delivery isn't handed to the dispatcher: it has no attempt.
	if queued[inserted[0].ID] {
		t.Fatal("enqueue reported an attempt for the deleted destination")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit after the delete: %v", err)
	}
	var deliveries, attempts int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM notification_deliveries WHERE id = 'delivery-race'),
		(SELECT count(*) FROM plugin_destination_delivery_attempts)`).Scan(&deliveries, &attempts); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 || attempts != 0 {
		t.Fatalf("after the race: %d deliveries, %d attempts; want the delivery kept and no attempt", deliveries, attempts)
	}
}

// An attempt's outcome, the auto-disable and its notice commit together: if
// any of it fails, none of it applies and the attempt runs again.
func TestPluginDestinationFinishIsAtomicDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	notices := recordNotices(pool, sender)
	noticeWriter := sender.noticeWriter
	sender.noticeWriter = func(context.Context) func(context.Context, pgx.Tx, Delivery) (*InsertedDelivery, error) {
		return func(context.Context, pgx.Tx, Delivery) (*InsertedDelivery, error) {
			return nil, errors.New("server stopped")
		}
	}
	d := createTestDestination(t, service, map[string]any{"topic": "alerts"})

	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, Message: "revoked"})
	attempt := processTestAttempt(t, pool, sender, d.ID, 1)
	got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID)
	if attempt.Outcome != WebhookOutcomeRetrying || attempt.AttemptNumber != 0 || !got.Enabled || got.ConsecutiveFailures != 0 {
		t.Fatalf("after a failed finish: attempt %+v, destination %+v; want nothing applied", attempt, got)
	}

	// Once the lease runs out the attempt runs again and finishes whole.
	sender.noticeWriter = noticeWriter
	if _, err := pool.Exec(ctx, `UPDATE plugin_destination_delivery_attempts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := sender.repo.ClaimDue(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("reclaim = %d, %v", len(claimed), err)
	}
	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, Message: "revoked"})
	sender.processAttempt(ctx, claimed[0])
	got, _ = sender.repo.GetByID(ctx, accountA, sharedID, d.ID)
	var inbox int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE type = $1`, DeliveryTypePluginDestinationAutoDisabled).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.ConsecutiveFailures != 1 || inbox != 1 || len(*notices) != 1 {
		t.Fatalf("after the retry: destination %+v, %d inbox notices, %d dispatched; want it disabled with one notice", got, inbox, len(*notices))
	}
}

// Settings this server can't decrypt (a wrong at-rest key) are a temporary
// failure, as for a webhook URL, and an edit is refused rather than saved
// over nothing, which would drop the stored secrets.
func TestPluginDestinationUnreadableSettingsDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	d := createTestDestination(t, service, map[string]any{"topic": "alerts", "token": "s3cret"})
	otherKey, err := secret.New([]byte("abcdefghijabcdefghijabcdefghijab"))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := otherKey.Encrypt(`{"topic":"alerts","token":"s3cret"}`, pluginDestinationConfigAAD(d.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE notification_plugin_destinations SET config_ciphertext = $2 WHERE id = $1`, d.ID, foreign); err != nil {
		t.Fatal(err)
	}

	attempt := processTestAttempt(t, pool, sender, d.ID, 1)
	requireRetryingAfter(t, attempt, 0)
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); !got.Enabled || len(host.sent) != 0 {
		t.Fatalf("after an unreadable send: destination %+v, %d sends; want it enabled and nothing sent", got, len(host.sent))
	}

	_, err = service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Settings: map[string]any{"topic": "news"}}, anyRevision)
	if !errors.Is(err, ErrPluginDestinationSettingsUnreadable) {
		t.Fatalf("edit of unreadable settings = %v, want ErrPluginDestinationSettingsUnreadable", err)
	}
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); got.ConfigCiphertext != foreign {
		t.Fatal("the refused edit changed the stored settings")
	}

	// An edit that doesn't touch settings never reads or rewrites them, so
	// it goes through and leaves the ciphertext as it was.
	renamed, off, quiet := "Laptop", false, false
	edited, err := service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Name: &renamed, Enabled: &off, NotifyNextUp: &quiet, NotifyRequests: &quiet}, anyRevision)
	if err != nil || edited.Name != renamed || edited.Enabled || edited.ConfigCiphertext != foreign {
		t.Fatalf("metadata edit of unreadable settings = %+v, %v; want it applied with the ciphertext unchanged", edited, err)
	}
}

// Text from a plugin can quote a credential. It is masked before it is
// stored, returned from a test, or returned as a validation refusal.
func TestPluginDestinationTextNeverCarriesSecretsDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	const token = "tk_live_8f3a"
	d := createTestDestination(t, service, map[string]any{"topic": "alerts", "token": token})

	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY, Message: "provider refused " + token})
	attempt := processTestAttempt(t, pool, sender, d.ID, 1)
	got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID)
	if got.LastFailureMessage == nil || strings.Contains(*got.LastFailureMessage, token) ||
		attempt.FailureMessage == nil || strings.Contains(*attempt.FailureMessage, token) {
		t.Fatalf("stored messages %v / %v carry the secret", got.LastFailureMessage, attempt.FailureMessage)
	}

	host.outcomes = append(host.outcomes, &pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_FAILED, Message: "bad key " + token})
	result, err := service.Test(ctx, accountA, sharedID, d.ID)
	if err != nil || result.OK || strings.Contains(result.Message, token) {
		t.Fatalf("test result %+v, %v; want a failure without the secret", result, err)
	}

	host.reject = token + " is not a valid token"
	_, err = service.UpdateGuarded(ctx, accountA, sharedID, d.ID, PluginDestinationInput{Settings: map[string]any{"topic": "news"}}, anyRevision)
	rejected, ok := errors.AsType[*PluginDestinationRejectedError](err)
	if !ok || strings.Contains(rejected.Message, token) {
		t.Fatalf("validation refusal = %v, want one without the secret", err)
	}
}

// Inbox retention keeps a delivery while its destination attempt can still
// run: deleting it would cascade to the attempt. A claimed attempt is kept
// however old, since a server is sending it under a live lease; waiting work
// only until the age cap, past which the attempt is finished without a send.
func TestPluginDestinationRetryOutlivesInboxRetentionDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	service, sender := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})
	d := createTestDestination(t, service, map[string]any{"topic": "alerts"})
	future := time.Now().Add(time.Hour)
	prune := func() {
		t.Helper()
		if _, err := sender.deliveries.DeleteOld(ctx, future, future, future); err != nil {
			t.Fatal(err)
		}
	}
	kept := func(deliveryID string) bool {
		t.Helper()
		row, err := sender.deliveries.GetRowByID(ctx, deliveryID)
		if err != nil {
			t.Fatal(err)
		}
		return row != nil
	}
	queue := func(age time.Duration) string {
		t.Helper()
		deliveryID := insertTestDelivery(t, pool, accountA, sharedID)
		enqueueTestAttempt(t, pool, sender.repo, deliveryID, d.ID)
		if _, err := pool.Exec(ctx, `UPDATE notification_deliveries SET created_at = now() - $2::interval WHERE id = $1`, deliveryID, age); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE plugin_destination_delivery_attempts SET attempted_at = now() - $2::interval WHERE notification_delivery_id = $1`, deliveryID, age); err != nil {
			t.Fatal(err)
		}
		return deliveryID
	}

	// Scheduled inside the window.
	scheduled := queue(time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE plugin_destination_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + interval '6 hours' WHERE notification_delivery_id = $1`, scheduled); err != nil {
		t.Fatal(err)
	}
	// Eight days old, claimed again just now: its lease is live.
	leased := queue(8 * 24 * time.Hour)
	if claimed, err := sender.repo.ClaimPendingForDelivery(ctx, leased); err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	// Waiting past the age cap, unclaimed: the sweep finishes such attempts.
	stranded := queue(pluginAttemptMaxAge + time.Hour)
	prune()
	if !kept(scheduled) || !kept(leased) || kept(stranded) {
		t.Fatalf("after pruning: scheduled kept %v, leased kept %v, stranded kept %v; want the first two kept",
			kept(scheduled), kept(leased), kept(stranded))
	}

	if _, err := pool.Exec(ctx, `UPDATE plugin_destination_delivery_attempts SET outcome = 'failed'`); err != nil {
		t.Fatal(err)
	}
	prune()
	if kept(scheduled) || kept(leased) {
		t.Fatal("deliveries whose attempts resolved weren't pruned")
	}
}

// dispatchRecorder records the rows a fanout hands its dispatchers.
type dispatchRecorder struct{ rows []DeliveryRow }

func (r *dispatchRecorder) Dispatch(_ context.Context, row DeliveryRow) error {
	r.rows = append(r.rows, row)
	return nil
}

// A new episode fanned out by the worker queues an attempt for the profile's
// destination in the fanout transaction, and only the delivery that got one
// is handed to the plugin destination dispatcher.
func TestFanoutQueuesPluginDestinationAttemptsDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	c := seedAccessCatalog(t, pool)
	seedFanoutEvent(t, pool, c, []string{sharedID, "kids"})
	service, sender := newTestPluginDestinationService(t, pool, &scriptedChannelHost{})
	name := "Phone"
	d, err := service.Create(ctx, accountA, sharedID, PluginDestinationInput{
		Channel: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"}, Name: &name,
		Settings: map[string]any{"topic": "alerts"},
	})
	if err != nil {
		t.Fatal(err)
	}

	worker := newAccessFanoutWorker(pool, scopeByProfile{sharedID: {}, "kids": {}})
	recorder := &dispatchRecorder{}
	worker.dispatcher = recorder
	worker.setPluginDestinationOutbox(sender, newProfileRateLimiter())
	if processed, err := worker.processBatch(ctx); err != nil || processed != 1 {
		t.Fatalf("processBatch = %d, %v; want 1 event", processed, err)
	}

	var deliveryID, outcome string
	if err := pool.QueryRow(ctx, `SELECT notification_delivery_id, outcome FROM plugin_destination_delivery_attempts WHERE destination_id = $1`, d.ID).
		Scan(&deliveryID, &outcome); err != nil || outcome != WebhookOutcomePending {
		t.Fatalf("queued attempt = %q %q, %v; want one pending attempt", deliveryID, outcome, err)
	}
	handed := map[string]bool{}
	for _, row := range recorder.rows {
		handed[row.ProfileID] = row.pluginAttempts
		if row.ProfileID == sharedID && row.ID != deliveryID {
			t.Fatalf("dispatched delivery %s, the attempt is for %s", row.ID, deliveryID)
		}
	}
	if len(recorder.rows) != 2 || !handed[sharedID] || handed["kids"] {
		t.Fatalf("dispatched %+v; want both deliveries, only the destination's marked for plugin dispatch", handed)
	}
}

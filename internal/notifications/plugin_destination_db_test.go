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

	"github.com/jackc/pgx/v5/pgxpool"

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
}

func (h *scriptedChannelHost) Channels(context.Context) ([]PluginChannel, error) {
	return []PluginChannel{{
		PluginChannelRef: PluginChannelRef{PluginID: "example.ntfy", CapabilityID: "ntfy"},
		DisplayName:      "ntfy",
		Schema: &pluginv1.ConfigSchema{
			JsonSchema: `{"type":"object","properties":{"topic":{"type":"string"},"token":{"type":"string"}}}`,
			AdminForm:  &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{Key: "topic"}, {Key: "token", Secret: true}}},
		},
		PublicFields: []string{"topic"},
		SecretFields: []string{"token"},
	}}, nil
}

func (h *scriptedChannelHost) ValidateDestination(context.Context, PluginChannelRef, *pluginv1.ValidateNotificationDestinationRequest) (*pluginv1.ValidateNotificationDestinationResponse, error) {
	h.mu.Lock()
	validating := h.validating
	h.validating = nil
	h.mu.Unlock()
	if validating != nil {
		validating()
	}
	return &pluginv1.ValidateNotificationDestinationResponse{Valid: true}, nil
}

func (h *scriptedChannelHost) SendNotification(_ context.Context, _ PluginChannelRef, req *pluginv1.SendNotificationRequest) (*pluginv1.SendNotificationResponse, error) {
	h.mu.Lock()
	during := h.during
	h.during = nil
	h.mu.Unlock()
	if during != nil {
		during()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unavailable {
		return nil, ErrPluginChannelUnavailable
	}
	h.sent = append(h.sent, req)
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
	return newPluginDestinationService(sender.repo, sender), sender
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
	if err := repo.EnqueueAttempts(t.Context(), tx, []DeliveryAttempt{{ID: "attempt-" + deliveryID, NotificationDeliveryID: deliveryID, TargetID: destinationID}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func anyRevision(int64) error { return nil }

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
	var notices []Delivery
	sender.operational = func(_ context.Context, d Delivery, opts OperationalDispatch) (*InsertedDelivery, error) {
		if opts.PluginDestinationFilter != nil || opts.WebhookFilter != nil {
			t.Errorf("auto-disable notice reaches destinations: %+v", opts)
		}
		notices = append(notices, d)
		return nil, nil
	}
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
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); !got.Enabled || got.DisabledReason != nil || len(notices) != 0 {
		t.Fatalf("after the plugin was off the destination is %+v with %d notices, want it enabled", got, len(notices))
	}

	invalid := send(&pluginv1.SendNotificationResponse{Outcome: pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, Message: "token revoked"})
	if invalid.Outcome != WebhookOutcomeFailed {
		t.Fatalf("after DESTINATION_INVALID the attempt is %s, want failed", invalid.Outcome)
	}
	got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID)
	if got.Enabled || got.DisabledReason == nil || !strings.Contains(*got.DisabledReason, "token revoked") {
		t.Fatalf("after DESTINATION_INVALID the destination is %+v, want disabled with the reason", got)
	}
	if len(notices) != 1 || notices[0].Type != DeliveryTypePluginDestinationAutoDisabled || notices[0].UserID != accountA {
		t.Fatalf("notices = %+v, want one auto-disable notice to the owner", notices)
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

// A send can outlive its lease, or race its owner's edit. Neither may let
// the stale send win: a lease another server took over keeps that server's
// result, and settings repaired during a failing send stay enabled.
func TestPluginDestinationStaleSendsDontWinDB(t *testing.T) {
	pool := pluginDestinationDB(t)
	ctx := t.Context()
	host := &scriptedChannelHost{}
	service, sender := newTestPluginDestinationService(t, pool, host)
	var notices int
	sender.operational = func(context.Context, Delivery, OperationalDispatch) (*InsertedDelivery, error) {
		notices++
		return nil, nil
	}
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
	sender.processAttempt(ctx, stale[0])
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
	if got, _ := sender.repo.GetByID(ctx, accountA, sharedID, d.ID); !got.Enabled || got.DisabledReason != nil || got.ConsecutiveFailures != 0 || notices != 0 {
		t.Fatalf("after a stale failure: destination %+v, %d notices; want the repaired destination enabled, with no failure counted and no notice", got, notices)
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
	if got.Name != renamed || conflict.Revision != got.Revision || stored["topic"] != "alerts" {
		t.Fatalf("after the conflict: name %q, revision %d (conflict names %d), topic %v; want the rename kept and the settings unchanged",
			got.Name, got.Revision, conflict.Revision, stored["topic"])
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

package notifications

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Profile IDs are unique only within an account: every account from before
// profiles has a profile "default". These tests give two accounts a profile
// with the same ID and check that one never sees or changes the other's
// notification destinations.

const (
	accountA = 1
	accountB = 2
	sharedID = "default"
)

// destinationAccountsDB returns a pool on a fresh schema holding the
// destination tables. notification_webhooks is built from its original
// migration plus the name-scope migration, so the test sees the production
// constraint name; the rest are copies of the current public tables.
func destinationAccountsDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := schemaDB(t, "notification_dest_")
	ctx := t.Context()
	data, err := os.ReadFile("../../migrations/sql/20260611120000_notification_webhooks.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, ddl, ok := strings.Cut(string(data), "CREATE TABLE public.notification_webhooks (")
	if !ok {
		t.Fatal("missing table declaration")
	}
	ddl, _, ok = strings.Cut(ddl, "\n);")
	if !ok {
		t.Fatal("missing table terminator")
	}
	if _, err = pool.Exec(ctx, "CREATE TABLE notification_webhooks ("+ddl+"\n);"+`
		ALTER TABLE notification_webhooks ADD COLUMN notify_requests boolean NOT NULL DEFAULT false, ADD COLUMN revision bigint NOT NULL DEFAULT 1;
		CREATE TABLE web_push_subscriptions (LIKE public.web_push_subscriptions INCLUDING ALL);
		CREATE TABLE web_push_delivery_attempts (LIKE public.web_push_delivery_attempts INCLUDING ALL);
		CREATE TABLE push_devices (LIKE public.push_devices INCLUDING ALL);
		CREATE TABLE android_push_installations (LIKE public.android_push_installations INCLUDING ALL);
		CREATE TABLE apple_push_installations (LIKE public.apple_push_installations INCLUDING ALL);
		CREATE TABLE notification_deliveries (LIKE public.notification_deliveries INCLUDING ALL);
		CREATE TABLE notification_preferences (LIKE public.notification_preferences INCLUDING ALL);
		CREATE TABLE notification_email_prefs (LIKE public.notification_email_prefs INCLUDING ALL);
		CREATE TABLE profile_series_interest (LIKE public.profile_series_interest INCLUDING ALL);
		CREATE TABLE profile_series_notification_mutes (LIKE public.profile_series_notification_mutes INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	up, _ := webhookNameMigration(t)
	if _, err = pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	return pool
}

// webhookNameMigration returns the name-scope migration's Up and Down SQL,
// unqualified so they apply to the test schema.
func webhookNameMigration(t *testing.T) (string, string) {
	t.Helper()
	data, err := os.ReadFile("../../migrations/sql/20261016163853_scope_webhook_names_to_account.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(strings.ReplaceAll(string(data), "public.", ""), "-- +goose Down")
	if !ok {
		t.Fatal("missing Down section")
	}
	return up, down
}

func TestLegacyPushRegistrationStaysOnTheAccount(t *testing.T) {
	pool := destinationAccountsDB(t)
	ctx := t.Context()
	repo := NewPushDeviceRepository(pool)
	cipher := testPushCipher(t)
	register := func(user int, device string) (*PushDevice, error) {
		t.Helper()
		registration, err := normalizeApplePushRegistration(ApplePushRegistrationInput{
			DeviceID: device, APNsToken: strings.Repeat("a", 64), APNsEnvironment: APNsEnvironmentProd,
			APNsTopic: ApplePushTopicSilo, PushMode: PushModePrivatePush,
		})
		if err != nil {
			t.Fatal(err)
		}
		registration.UserID, registration.ProfileID = user, sharedID
		return repo.UpsertApple(ctx, registration, cipher)
	}
	owners := func(device string) []int {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT user_id FROM push_devices WHERE device_id = $1 ORDER BY user_id`, device)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	if _, err := register(accountA, "shared-phone"); err != nil {
		t.Fatal(err)
	}
	// A supplied identifier and provider token do not authorize account transfer.
	if _, err := register(accountB, "shared-phone"); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("foreign registration = %v", err)
	}
	if err := repo.DeleteByProfileDevice(ctx, accountB, sharedID, "shared-phone"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owners("shared-phone"), []int{accountA}) {
		t.Fatalf("foreign registration or unregister changed ownership: %v", owners("shared-phone"))
	}
	service := NewPushDeviceService(repo, cipher)
	if err := service.Unregister(ctx, accountA, sharedID, "shared-phone"); err != nil {
		t.Fatal(err)
	}
	if got := owners("shared-phone"); len(got) != 0 {
		t.Fatalf("account A's unregister left %v", got)
	}
	if _, err := register(accountB, "shared-phone"); err != nil {
		t.Fatalf("registration after owner unregisters: %v", err)
	}
}

// TestWebhookRejectionStreak covers the three-consecutive-4xx auto-disable
// rule: only rejected deliveries extend the streak, re-enabling and success
// reset it, and concurrent disables agree on one winner so one notice posts.
func TestWebhookRejectionStreak(t *testing.T) {
	pool := schemaDB(t, "notification_webhook_streak_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE notification_webhooks (LIKE public.notification_webhooks INCLUDING ALL);
		CREATE TABLE webhook_delivery_attempts (LIKE public.webhook_delivery_attempts INCLUDING ALL);
		INSERT INTO notification_webhooks (id, user_id, profile_id, name, type, url_ciphertext, url_host)
		VALUES ('wh', 1, 'default', 'Discord', 'discord', 'x', 'discord.com')`); err != nil {
		t.Fatal(err)
	}
	repo := NewWebhookRepository(pool)
	status := 404
	attempts := 0
	// newAttempt queues an attempt for the next send to finalize.
	newAttempt := func() string {
		t.Helper()
		attempts++
		id := "attempt-" + strconv.Itoa(attempts)
		if _, err := pool.Exec(ctx, `
			INSERT INTO webhook_delivery_attempts (id, notification_delivery_id, webhook_id, attempt_number, outcome)
			VALUES ($1, $1, 'wh', 0, 'pending')`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	fail := func(rejected bool, want int) {
		t.Helper()
		done := WebhookAttemptOutcome{AttemptID: newAttempt(), Outcome: WebhookOutcomeFailed,
			AttemptNumber: 1, HTTPStatus: &status, Message: "rejected"}
		got, err := repo.RecordFailure(ctx, "wh", done, rejected)
		if err != nil || got != want {
			t.Fatalf("RecordFailure(rejected=%v) = %d, %v; want %d", rejected, got, err, want)
		}
	}
	fail(true, 1)
	fail(false, 1) // a retryable failure is not a rejection
	fail(true, 2)
	fail(true, 3)

	if disabled, err := repo.Disable(ctx, "wh", "rejected"); err != nil || !disabled {
		t.Fatalf("first Disable = %v, %v; want true", disabled, err)
	}
	if disabled, err := repo.Disable(ctx, "wh", "rejected"); err != nil || disabled {
		t.Fatalf("second Disable = %v, %v; want false", disabled, err)
	}

	if _, err := repo.UpdateGuarded(ctx, 1, "default", "wh", func(hook *Webhook) error {
		hook.Enabled, hook.ConsecutiveFailures, hook.DisabledReason = true, 0, nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fail(true, 1) // re-enabled: the old rejections do not count

	// A send another worker already finished (its lease expired mid-send)
	// changes neither the attempt nor the streak.
	late := WebhookAttemptOutcome{AttemptID: "attempt-" + strconv.Itoa(attempts), Outcome: WebhookOutcomeFailed,
		AttemptNumber: 1, HTTPStatus: &status}
	if _, err := repo.RecordFailure(ctx, "wh", late, true); !errors.Is(err, errAttemptSuperseded) {
		t.Fatalf("second finalize of one send = %v; want errAttemptSuperseded", err)
	}
	fail(true, 2)

	delivered := WebhookAttemptOutcome{AttemptID: newAttempt(), Outcome: WebhookOutcomeDelivered, AttemptNumber: 1}
	if err := repo.RecordSuccess(ctx, "wh", delivered); err != nil {
		t.Fatal(err)
	}
	fail(true, 1)

	// Each send's outcome lands with its streak update.
	var unfinished int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM webhook_delivery_attempts WHERE outcome = 'pending'`).Scan(&unfinished); err != nil || unfinished != 0 {
		t.Fatalf("pending attempts = %d, %v; want every attempt finalized", unfinished, err)
	}
}

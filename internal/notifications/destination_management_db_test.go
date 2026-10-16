package notifications

import (
	"errors"
	"os"
	"slices"
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

func seedAccountWebhook(t *testing.T, repo *WebhookRepository, user int, id, name string, limit int) error {
	t.Helper()
	return repo.InsertWithLimit(t.Context(), Webhook{
		ID: id, UserID: user, ProfileID: sharedID, Name: name, Type: WebhookTypeGeneric,
		URLCiphertext: "url-cipher", URLHost: "example.test", SigningSecretCiphertext: new("secret-cipher"), Enabled: true,
	}, limit)
}

func webhookIDs(hooks []Webhook) []string {
	ids := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		ids = append(ids, hook.ID)
	}
	return ids
}

func TestWebhookManagementStaysOnTheAccount(t *testing.T) {
	pool := destinationAccountsDB(t)
	ctx := t.Context()
	repo := NewWebhookRepository(pool)
	if err := seedAccountWebhook(t, repo, accountB, "b-hook", "Alerts", 1); err != nil {
		t.Fatalf("account B insert: %v", err)
	}
	// Account A's profile is under its own cap of one and may reuse the name.
	if err := seedAccountWebhook(t, repo, accountA, "a-hook", "Alerts", 1); err != nil {
		t.Fatalf("account A insert with B's name, at B's cap: %v", err)
	}
	if err := seedAccountWebhook(t, repo, accountA, "a-hook-2", "Alerts", 2); !errors.Is(err, ErrWebhookNameTaken) {
		t.Fatalf("duplicate name on one account = %v, want ErrWebhookNameTaken", err)
	}
	if err := seedAccountWebhook(t, repo, accountA, "a-hook-2", "Other", 1); !errors.Is(err, ErrWebhookLimit) {
		t.Fatalf("over the cap = %v, want ErrWebhookLimit", err)
	}

	listed, err := repo.ListByProfile(ctx, accountA, sharedID)
	if err != nil || !slices.Equal(webhookIDs(listed), []string{"a-hook"}) {
		t.Fatalf("ListByProfile(A) = %v %v, want only a-hook", webhookIDs(listed), err)
	}
	page, err := repo.ListPage(ctx, accountA, sharedID, 10, nil)
	if err != nil || !slices.Equal(webhookIDs(page), []string{"a-hook"}) {
		t.Fatalf("ListPage(A) = %v %v, want only a-hook", webhookIDs(page), err)
	}
	if hook, err := repo.GetByID(ctx, accountA, sharedID, "b-hook"); err != nil || hook != nil {
		t.Fatalf("GetByID(A, b-hook) = %+v %v, want none", hook, err)
	}
	if _, err := repo.UpdateGuarded(ctx, accountA, sharedID, "b-hook", func(*Webhook) error {
		t.Error("account B's webhook reached account A's editor")
		return nil
	}); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("UpdateGuarded(A, b-hook) = %v, want ErrWebhookNotFound", err)
	}
	if err := repo.ReplaceSigningSecret(ctx, accountA, sharedID, "b-hook", "a-secret"); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("ReplaceSigningSecret(A, b-hook) = %v, want ErrWebhookNotFound", err)
	}
	if err := repo.DeleteGuarded(ctx, accountA, sharedID, "b-hook", func(int64) error { return nil }); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("DeleteGuarded(A, b-hook) = %v, want ErrWebhookNotFound", err)
	}
	// The bridge delete is idempotent: it succeeds and removes nothing.
	if err := repo.Delete(ctx, accountA, sharedID, "b-hook"); err != nil {
		t.Fatalf("Delete(A, b-hook): %v", err)
	}
	hook, err := repo.GetByID(ctx, accountB, sharedID, "b-hook")
	if err != nil || hook == nil || *hook.SigningSecretCiphertext != "secret-cipher" {
		t.Fatalf("account B's webhook after A's calls = %+v %v, want it unchanged", hook, err)
	}

	// Renaming into a name the same account profile holds is still refused.
	if err := seedAccountWebhook(t, repo, accountB, "b-hook-2", "Other", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateGuarded(ctx, accountB, sharedID, "b-hook-2", func(h *Webhook) error {
		h.Name = "Alerts"
		return nil
	}); !errors.Is(err, ErrWebhookNameTaken) {
		t.Fatalf("rename onto a taken name = %v, want ErrWebhookNameTaken", err)
	}

	// Down restores the profile-ID-wide constraint; with both accounts
	// holding "Alerts" it fails and leaves the table as it was.
	_, down := webhookNameMigration(t)
	if _, err := pool.Exec(ctx, down); err == nil {
		t.Fatal("Down succeeded although two accounts share a webhook name")
	}
	if err := seedAccountWebhook(t, repo, accountB, "b-hook-3", "Alerts", 3); !errors.Is(err, ErrWebhookNameTaken) {
		t.Fatalf("after the failed Down, duplicate = %v, want ErrWebhookNameTaken", err)
	}
}

func TestWebPushManagementStaysOnTheAccount(t *testing.T) {
	pool := destinationAccountsDB(t)
	ctx := t.Context()
	repo := NewWebPushRepository(pool)
	for _, sub := range []WebPushSubscription{
		{ID: "a-sub", UserID: accountA, ProfileID: sharedID, Endpoint: "https://push.example.test/a", P256dh: "key", Auth: "auth"},
		{ID: "b-sub", UserID: accountB, ProfileID: sharedID, Endpoint: "https://push.example.test/b", P256dh: "key", Auth: "auth"},
	} {
		if _, err := repo.Upsert(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := repo.ListByProfile(ctx, accountA, sharedID)
	if err != nil || len(listed) != 1 || listed[0].ID != "a-sub" {
		t.Fatalf("ListByProfile(A) = %+v %v, want only a-sub", listed, err)
	}
	page, err := repo.ListPage(ctx, accountA, sharedID, 10, nil)
	if err != nil || len(page) != 1 || page[0].ID != "a-sub" {
		t.Fatalf("ListPage(A) = %+v %v, want only a-sub", page, err)
	}
	service := &WebPushService{repo: repo}
	if err := service.Unsubscribe(ctx, accountA, sharedID, "b-sub", ""); err != nil {
		t.Fatalf("Unsubscribe(A, b-sub): %v", err)
	}
	if got, err := repo.ListByProfile(ctx, accountB, sharedID); err != nil || len(got) != 1 || got[0].ID != "b-sub" {
		t.Fatalf("account B after A's delete = %+v %v, want b-sub kept", got, err)
	}

	// Account A signs in on the browser that holds B's endpoint. The row moves
	// to A and B's undelivered attempts are canceled, not sent to A.
	if _, err := pool.Exec(ctx, `INSERT INTO web_push_delivery_attempts (id, notification_delivery_id, subscription_id, attempt_number, outcome)
		VALUES ('b-pending', 'b-delivery', 'b-sub', 1, 'pending'), ('b-done', 'b-delivery-old', 'b-sub', 1, 'delivered')`); err != nil {
		t.Fatal(err)
	}
	moved, err := repo.Upsert(ctx, WebPushSubscription{ID: "new-id", UserID: accountA, ProfileID: sharedID, Endpoint: "https://push.example.test/b", P256dh: "key2", Auth: "auth2"})
	if err != nil || moved.ID != "b-sub" || moved.UserID != accountA {
		t.Fatalf("reassign = %+v %v", moved, err)
	}
	rows, err := pool.Query(ctx, `SELECT id FROM web_push_delivery_attempts ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	left, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(left, []string{"b-done"}) {
		t.Fatalf("attempts after reassignment = %v, want only the delivered one", left)
	}
}

func TestLegacyPushRegistrationStaysOnTheAccount(t *testing.T) {
	pool := destinationAccountsDB(t)
	ctx := t.Context()
	repo := NewPushDeviceRepository(pool)
	cipher := testPushCipher(t)
	register := func(user int, device string) *PushDevice {
		t.Helper()
		registration, err := normalizeApplePushRegistration(ApplePushRegistrationInput{
			DeviceID: device, APNsToken: strings.Repeat("a", 64), APNsEnvironment: APNsEnvironmentProd,
			APNsTopic: ApplePushTopicSilo, PushMode: PushModePrivatePush,
		})
		if err != nil {
			t.Fatal(err)
		}
		registration.UserID, registration.ProfileID = user, sharedID
		row, err := repo.UpsertApple(ctx, registration, cipher)
		if err != nil {
			t.Fatal(err)
		}
		return row
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

	first := register(accountA, "shared-phone")
	// Account B signs in on the same install: A's registration (and the
	// attempts that cascade with it) goes, B gets a new row.
	second := register(accountB, "shared-phone")
	if second.ID == first.ID || !slices.Equal(owners("shared-phone"), []int{accountB}) {
		t.Fatalf("after B registers: row %s (A's was %s), owners %v; want a new row for B only", second.ID, first.ID, owners("shared-phone"))
	}
	if err := repo.DeleteByProfileDevice(ctx, accountA, sharedID, "shared-phone"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owners("shared-phone"), []int{accountB}) {
		t.Fatalf("account A's unregister removed account B's registration: %v", owners("shared-phone"))
	}
	service := NewPushDeviceService(repo, cipher)
	if err := service.Unregister(ctx, accountB, sharedID, "shared-phone"); err != nil {
		t.Fatal(err)
	}
	if got := owners("shared-phone"); len(got) != 0 {
		t.Fatalf("account B's unregister left %v", got)
	}
}

func TestPurgeProfileKeepsAnotherAccountsSameIDProfile(t *testing.T) {
	pool := destinationAccountsDB(t)
	ctx := t.Context()
	hooks := NewWebhookRepository(pool)
	pushes := NewWebPushRepository(pool)
	devices := NewPushDeviceRepository(pool)
	if err := seedAccountWebhook(t, hooks, accountA, "a-hook", "Alerts", 10); err != nil {
		t.Fatal(err)
	}
	if err := seedAccountWebhook(t, hooks, accountB, "b-hook", "Alerts", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO web_push_subscriptions (id, user_id, profile_id, endpoint, p256dh, auth)
			VALUES ('sub-1', 1, 'default', 'https://push.example.test/1', 'k', 'a'), ('sub-2', 2, 'default', 'https://push.example.test/2', 'k', 'a');
		INSERT INTO push_devices (id, user_id, profile_id, device_id, platform, provider, apns_environment, apns_topic, apns_token_ciphertext, apns_token_hash, server_device_id)
			VALUES ('dev-1', 1, 'default', 'phone-1', 'apple', 'silo_relay', 'production', 'topic', 'c', 'h1', 's1'),
			       ('dev-2', 2, 'default', 'phone-2', 'apple', 'silo_relay', 'production', 'topic', 'c', 'h2', 's2');
		INSERT INTO notification_deliveries (id, user_id, profile_id, type, reason_flags)
			VALUES ('del-1', 1, 'default', 'request.approved', '{}'), ('del-2', 2, 'default', 'request.approved', '{}');
		INSERT INTO notification_email_prefs (profile_id, user_id) VALUES ('default', 2)`); err != nil {
		t.Fatal(err)
	}
	s := &System{
		Interests:      NewInterestRepository(pool),
		Deliveries:     NewDeliveryRepository(pool),
		Preferences:    NewPreferencesRepository(pool),
		EmailPrefs:     NewEmailPrefsRepository(pool),
		webhookRepo:    hooks,
		webPushRepo:    pushes,
		pushDeviceRepo: devices,
	}
	if err := s.PurgeProfile(ctx, accountA, sharedID); err != nil {
		t.Fatalf("PurgeProfile(A): %v", err)
	}
	for _, table := range []string{"notification_webhooks", "web_push_subscriptions", "push_devices", "notification_deliveries", "notification_email_prefs"} {
		var a, b int
		if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE user_id = 1), count(*) FILTER (WHERE user_id = 2) FROM `+table).Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		if a != 0 || b != 1 {
			t.Errorf("%s after purging account A's profile: A=%d B=%d, want A=0 B=1", table, a, b)
		}
	}
}

package notifications

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

// emailAccountsDB returns a pool on a fresh schema holding the email
// notification tables, migrated to the account key, and accounts A and B.
func emailAccountsDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := schemaDB(t, "notification_email_")
	if _, err := pool.Exec(t.Context(), `CREATE TABLE users (id integer PRIMARY KEY, username text, email text);
		INSERT INTO users VALUES (1, 'a', 'a-login@example.test'), (2, 'b', 'b-login@example.test')`); err != nil {
		t.Fatal(err)
	}
	preAccountKeyEmailTables(t, pool)
	return pool
}

func TestEmailPrefsStayOnTheAccountDB(t *testing.T) {
	pool := emailAccountsDB(t)
	ctx := t.Context()
	up, _ := emailAccountKeyMigration(t)
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	repo := NewEmailPrefsRepository(pool)
	get := func(user int) EmailPrefs {
		t.Helper()
		prefs, err := repo.Get(ctx, user, sharedID)
		if err != nil {
			t.Fatal(err)
		}
		return prefs
	}
	checkB := func(step string) {
		t.Helper()
		if got := get(accountB); got.UserID != accountB || got.Mode != EmailModePerEpisode || got.CustomEmail != "b@example.test" {
			t.Fatalf("%s changed account B's email settings: %+v", step, got)
		}
	}

	_, hash, err := newEmailToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.RequestPendingAddress(ctx, accountB, sharedID, "b@example.test", hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if outcome, err := repo.ConsumeVerifyToken(ctx, hash); err != nil || outcome != EmailVerifyOK {
		t.Fatalf("account B verification = %v, %v", outcome, err)
	}
	if err = repo.SetMode(ctx, accountB, sharedID, EmailModePerEpisode); err != nil {
		t.Fatal(err)
	}
	if got := get(accountA); got.Mode != EmailModeOff || got.CustomEmail != "" {
		t.Fatalf("account A sees account B's email settings: %+v", got)
	}

	if err = repo.SetMode(ctx, accountA, sharedID, EmailModeDailyDigest); err != nil {
		t.Fatal(err)
	}
	checkB("account A's mode change")
	// A verified address still serves one profile on the whole server.
	if err = repo.RequestPendingAddress(ctx, accountA, sharedID, "B@example.test", "unused", time.Now().Add(time.Hour)); !errors.Is(err, ErrEmailAddressInUse) {
		t.Fatalf("account A claiming account B's address = %v", err)
	}

	receipt, err := repo.QueueVerification(ctx, EmailVerificationIntent{
		ID: uuid.NewString(), UserID: accountA, ProfileID: sharedID, Address: "a@example.test",
		ProfileName: "Synthetic profile", LinkBase: "https://example.test",
	}, testPushCipher(t))
	if err != nil || !receipt.Current {
		t.Fatalf("account A queue = %+v, %v", receipt, err)
	}
	claim, err := repo.ClaimVerificationDispatch(ctx, time.Now())
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, %v", claim, err)
	}
	if !claim.PrefsFound || claim.PrefsUser != accountA || claim.PrefsHash != claim.TokenHash {
		t.Fatalf("claim read another row's pending state: %+v", claim)
	}
	if outcome, err := repo.ConsumeVerifyToken(ctx, claim.TokenHash); err != nil || outcome != EmailVerifyOK {
		t.Fatalf("account A verification = %v, %v", outcome, err)
	}
	if got := get(accountA); got.Mode != EmailModeDailyDigest || got.CustomEmail != "a@example.test" {
		t.Fatalf("account A's email settings = %+v", got)
	}
	checkB("account A's verification")

	if err = repo.ClearCustomAddress(ctx, accountA, sharedID); err != nil {
		t.Fatal(err)
	}
	checkB("account A's address removal")
	if err = repo.DeleteForProfile(ctx, accountA, sharedID); err != nil {
		t.Fatal(err)
	}
	checkB("deleting account A's profile")
}

// The upgrade cannot tell which account verified the address on a row two
// accounts' profiles shared, so it resets that row and keeps every other row
// as it was. Going back refuses once both accounts have a row.
func TestEmailPrefsAccountKeyMigrationResetsSharedRowsDB(t *testing.T) {
	pool := emailAccountsDB(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_email_prefs
			(profile_id, user_id, mode, custom_email, pending_email, pending_token_hash, pending_expires_at, unsubscribe_token)
		VALUES ('default', 1, 'per_episode', 'b@example.test', 'b2@example.test', 'hash', now() + interval '1 hour', 'unsubscribe-shared'),
		       ('kids', 1, 'daily_digest', 'kids@example.test', '', '', NULL, 'unsubscribe-kids');
		INSERT INTO user_profiles VALUES (1, 'default'), (1, 'kids'), (2, 'default')`); err != nil {
		t.Fatal(err)
	}
	up, down := emailAccountKeyMigration(t)
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	type row struct {
		Mode, Custom, Pending, PendingHash, Unsubscribe string
		Expires                                         *time.Time
	}
	read := func(profile string) row {
		t.Helper()
		var r row
		if err := pool.QueryRow(ctx, `SELECT mode, custom_email, pending_email, pending_token_hash, unsubscribe_token, pending_expires_at
			FROM notification_email_prefs WHERE user_id = 1 AND profile_id = $1`, profile).
			Scan(&r.Mode, &r.Custom, &r.Pending, &r.PendingHash, &r.Unsubscribe, &r.Expires); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if got := read("default"); got != (row{Mode: EmailModeOff}) {
		t.Fatalf("shared row kept settings: %+v", got)
	}
	if got := read("kids"); got.Mode != EmailModeDailyDigest || got.Custom != "kids@example.test" || got.Unsubscribe != "unsubscribe-kids" {
		t.Fatalf("unshared row changed: %+v", got)
	}

	if err := NewEmailPrefsRepository(pool).SetMode(ctx, accountB, sharedID, EmailModePerEpisode); err != nil {
		t.Fatalf("account B's own row: %v", err)
	}
	if _, err := pool.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "cannot be keyed by profile id alone") {
		t.Fatalf("down with two accounts' rows = %v", err)
	}
}

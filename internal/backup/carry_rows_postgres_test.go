package backup

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
)

// TestCarryMatchesAProfileWithinItsAccountDB: a profile id is unique only
// within an account, so a live row naming a profile is carried only when its
// own account has that profile in the restored data, even where another
// account has a profile with the same id.
func TestCarryMatchesAProfileWithinItsAccountDB(t *testing.T) {
	live, liveURL := backuptest.Database(t)
	restored, restoredURL := backuptest.Database(t)
	ctx := t.Context()
	account := func(name string) (id int) {
		t.Helper()
		if err := live.QueryRow(ctx, `INSERT INTO users (role, username, created_at) VALUES ('user', $1, '2026-01-02 03:04:05.678901+00') RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		// The same account in the restored data: its id and creation time (sameAccount).
		dbExec(t, restored, `INSERT INTO users (id, role, username, created_at) VALUES ($1, 'user', $2, '2026-01-02 03:04:05.678901+00')`, id, name)
		return id
	}
	first, second := account("first"), account("second")
	for _, db := range []*pgxpool.Pool{live, restored} {
		testProfile(t, db, first, "kids", "Kids", false, "")
		testProfile(t, db, second, "default", "Default", false, "")
	}
	// Since the backup, the second account added a profile with the id
	// the first account's profile has, and destinations for it.
	testProfile(t, live, second, "kids", "Kids", false, "")
	webhook := func(id, profile string) {
		dbExec(t, live, `INSERT INTO notification_webhooks (id, user_id, profile_id, name, type, url_ciphertext, url_host)
			VALUES ($1, $2, $3, $1, 'discord', 'cipher', 'example.invalid')`, id, second, profile)
	}
	webhook("hook-kids", "kids")
	webhook("hook-default", "default")
	connection := func(id, profile string) {
		dbExec(t, live, `INSERT INTO webhook_sync_connections (id, user_id, provider, default_profile_id, webhook_secret)
			VALUES ($1::uuid, $2, 'plex', $3, $1::text)`, id, second, profile)
	}
	const kidsConnection, defaultConnection = "00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b"
	connection(kidsConnection, "kids")
	connection(defaultConnection, "default")
	mapping := func(profile string) (id int) {
		t.Helper()
		if err := live.QueryRow(ctx, `INSERT INTO webhook_sync_profile_mappings (connection_id, external_user_id, silo_profile_id)
			VALUES ($1, $2, $2) RETURNING id`, defaultConnection, profile).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	kidsMapping, defaultMapping := mapping("kids"), mapping("default")

	carried, err := PGLoader{}.CarryCredentials(ctx, restoredURL, liveURL, AppKeysKept)
	if err != nil {
		t.Fatal(err)
	}
	wantDropped := []string{
		fmt.Sprintf("notification webhook hook-kids 'hook-kids' (account %d)", second),
		fmt.Sprintf("webhook sync connection %s '' (account %d)", kidsConnection, second),
		fmt.Sprintf("webhook sync mapping %d of connection %s", kidsMapping, defaultConnection),
	}
	for _, want := range wantDropped {
		if !slices.Contains(carried.Dropped, want) {
			t.Errorf("dropped %q, want %q among them", carried.Dropped, want)
		}
	}
	ids := func(sql string) []string {
		t.Helper()
		got, err := collectStrings(ctx, restored, sql)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := ids(`SELECT id FROM notification_webhooks ORDER BY id`); !slices.Equal(got, []string{"hook-default"}) {
		t.Errorf("restored webhooks %v, want only the one for a profile of its account", got)
	}
	if got := ids(`SELECT id::text FROM webhook_sync_connections ORDER BY id`); !slices.Equal(got, []string{defaultConnection}) {
		t.Errorf("restored webhook sync connections %v, want only the one whose default profile is its account's", got)
	}
	if got := ids(`SELECT id::text FROM webhook_sync_profile_mappings ORDER BY id`); !slices.Equal(got, []string{strconv.Itoa(defaultMapping)}) {
		t.Errorf("restored webhook sync mappings %v, want only the one naming a profile of its account", got)
	}
}

// A backup taken before notification_email_prefs was keyed by account holds
// one row per profile id. The carry upserts on the key that backup has, and
// drops the live rows two accounts' profiles with the same id hold instead
// of keeping one of them there.
func TestCarryOntoAnOlderKeyDropsSharedRowsDB(t *testing.T) {
	pool, dsn := testDB(t)
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, createDatabase(t, pool, dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, `
		CREATE TABLE notification_email_prefs (
			profile_id text PRIMARY KEY, user_id integer NOT NULL, custom_email text NOT NULL DEFAULT '');
		CREATE TABLE user_profiles (id text, user_id integer);
		CREATE TEMP TABLE carried_owners (id bigint);
		INSERT INTO notification_email_prefs VALUES ('default', 1, 'stale@example.test');
		INSERT INTO user_profiles VALUES ('default', 1), ('default', 2), ('kids', 1);
		INSERT INTO carried_owners VALUES (1), (2)`); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(secretTables, func(s secretTable) bool { return s.name == "notification_email_prefs" })
	live := `[{"user_id": 1, "profile_id": "default", "custom_email": "a@example.test"},
		{"user_id": 2, "profile_id": "default", "custom_email": "b@example.test"},
		{"user_id": 1, "profile_id": "kids", "custom_email": "kids@example.test"}]`
	result := CarriedCredentials{Kept: map[string]int{}}
	if err = secretTables[i].carryInto(ctx, tx, live, &result); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `SELECT profile_id || ' ' || user_id || ' ' || custom_email FROM notification_email_prefs ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"kids 1 kids@example.test"}) || len(result.Dropped) != 2 || result.Kept["notification_email_prefs"] != 1 {
		t.Fatalf("carried %v, dropped %v, kept %v", got, result.Dropped, result.Kept)
	}

	// A live row the backup would not carry anyway (its account is not in
	// the backup) does not cost the other account its row.
	if _, err = tx.Exec(ctx, `DELETE FROM carried_owners WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	result = CarriedCredentials{Kept: map[string]int{}}
	if err = secretTables[i].carryInto(ctx, tx, live, &result); err != nil {
		t.Fatal(err)
	}
	if rows, err = tx.Query(ctx, `SELECT profile_id || ' ' || user_id || ' ' || custom_email FROM notification_email_prefs ORDER BY 1`); err != nil {
		t.Fatal(err)
	}
	if got, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"default 1 a@example.test", "kids 1 kids@example.test"}) || len(result.Dropped) != 1 {
		t.Fatalf("with account 2 not in the backup: carried %v, dropped %v", got, result.Dropped)
	}
}

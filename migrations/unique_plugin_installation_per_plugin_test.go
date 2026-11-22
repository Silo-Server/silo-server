package migrations

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The duplicate cleanup keeps a break-glass account's local sessions and API
// keys, as a provider refusal does, and moves a deleted duplicate's marker
// provider settings to the kept installation.
func TestUniquePluginInstallationCleanupPostgres(t *testing.T) {
	const migration = "20261118203050_unique_plugin_installation_per_plugin"
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	// The migration runs against the migrated schema with its index dropped,
	// in a transaction that is rolled back.
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	migrationExec(t, tx, `DROP INDEX idx_plugin_installations_plugin_id`)

	suffix := time.Now().UnixNano()
	pluginID := fmt.Sprintf("test.unique-install-%d", suffix)
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := tx.QueryRow(t.Context(), sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	install := func() int64 {
		return scalar(`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled)
VALUES ($1, '1.0.0', '/plugins/x', true) RETURNING id`, pluginID)
	}
	kept, dropped := install(), install()
	// The kept row holds the enabled sign-in binding; the dropped row only
	// identities and a marker provider setting.
	migrationExec(t, tx, fmt.Sprintf(`INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES (%d, 'oidc', true)`, kept))

	user := func(name string, breakGlass bool) int64 {
		return scalar(`INSERT INTO users (username, role, break_glass) VALUES ($1, 'admin', $2) RETURNING id`,
			fmt.Sprintf("%s-%d", name, suffix), breakGlass)
	}
	regular, breakGlass := user("regular", false), user("breakglass", true)
	identity := func(userID int64) int64 {
		return scalar(`INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
VALUES ($1, $2, $3) RETURNING id`, dropped, fmt.Sprintf("subject-%d", userID), userID)
	}
	session := func(id string, userID int64, identityID *int64) {
		t.Helper()
		if _, err := tx.Exec(t.Context(), `INSERT INTO auth_sessions (id, user_id, identity_id, expires_at)
VALUES ($1, $2, $3, now() + interval '1 day')`, fmt.Sprintf("%s-%d", id, suffix), userID, identityID); err != nil {
			t.Fatal(err)
		}
	}
	apiKey := func(userID int64) {
		t.Helper()
		if _, err := tx.Exec(t.Context(), `INSERT INTO api_keys (user_id, label, key_hash) VALUES ($1, 'k', encode(sha256($2::bytea), 'hex'))`,
			userID, fmt.Sprintf("key-%d-%d", userID, suffix)); err != nil {
			t.Fatal(err)
		}
	}
	regularIdentity, breakGlassIdentity := identity(regular), identity(breakGlass)
	session("regular-provider", regular, &regularIdentity)
	session("regular-local", regular, nil)
	session("breakglass-provider", breakGlass, &breakGlassIdentity)
	session("breakglass-local", breakGlass, nil)
	apiKey(regular)
	apiKey(breakGlass)
	// An Audiobookshelf token does not record the login it was minted from,
	// so it may have come from the deleted identity.
	absToken := scalar(`INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'd') RETURNING id`,
		breakGlass, fmt.Sprintf("abs-%d", suffix))
	migrationExec(t, tx, fmt.Sprintf(`INSERT INTO marker_provider_config (provider, fetch_priority, contribute_enabled)
VALUES ('plugin:%d:markers', 7, false)`, dropped))

	migrationExec(t, tx, adminMigrationSQL(t, migration, "public", false))

	if n := scalar(`SELECT count(*) FROM plugin_installations WHERE plugin_id = $1`, pluginID); n != 1 {
		t.Fatalf("installations left = %d, want 1", n)
	}
	if n := scalar(`SELECT count(*) FROM plugin_installations WHERE id = $1`, kept); n != 1 {
		t.Fatal("the row with the enabled sign-in binding was not kept")
	}
	live := func(id string) bool {
		return scalar(`SELECT count(*) FROM auth_sessions WHERE id = $1 AND revoked_at IS NULL`, fmt.Sprintf("%s-%d", id, suffix)) == 1
	}
	for id, want := range map[string]bool{
		"regular-provider":    false,
		"regular-local":       false,
		"breakglass-provider": false,
		"breakglass-local":    true,
	} {
		if got := live(id); got != want {
			t.Errorf("session %s live = %v, want %v", id, got, want)
		}
	}
	keys := func(userID int64) int64 {
		return scalar(`SELECT count(*) FROM api_keys WHERE user_id = $1`, userID)
	}
	if n := keys(regular); n != 0 {
		t.Errorf("regular account kept %d API keys, want 0", n)
	}
	if n := keys(breakGlass); n != 1 {
		t.Errorf("break-glass account has %d API keys, want 1", n)
	}
	if n := scalar(`SELECT count(*) FROM abs_sessions WHERE id = $1 AND revoked_at IS NULL`, absToken); n != 0 {
		t.Error("break-glass account kept an Audiobookshelf token that may come from the deleted identity")
	}
	var priority int64
	var contribute bool
	if err := tx.QueryRow(t.Context(), `SELECT fetch_priority, contribute_enabled FROM marker_provider_config WHERE provider = $1`,
		fmt.Sprintf("plugin:%d:markers", kept)).Scan(&priority, &contribute); err != nil {
		t.Fatalf("kept installation's marker provider setting: %v", err)
	}
	if priority != 7 || contribute {
		t.Errorf("marker provider setting = priority %d, contribute %v; want 7, false", priority, contribute)
	}
}

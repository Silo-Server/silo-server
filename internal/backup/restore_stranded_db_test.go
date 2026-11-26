package backup

import (
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/appkeys/appkeystest"
	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/config"
)

// A restored database with local passwords off names every enabled account
// it leaves with no way in, not only key holders: a password-only account
// is listed, and is not once it has a provider account it can sign in with,
// which an OAuth provider without a public URL and a network provider are
// not; a break-glass administrator, a disabled account and a key holder
// whose key still works are not listed.
func TestStrandedAccountsCoverEveryAccountDB(t *testing.T) {
	db, dbURL := backuptest.Database(t)
	dbExec(t, db, `INSERT INTO server_settings (key, value) VALUES ($1, 'false')`, config.AuthLocalPasswordLoginSettingKey)
	account := func(name, role string, password, breakGlass, enabled bool) int64 {
		t.Helper()
		var hash *string
		if password {
			hash = new("$2a$10$placeholder")
		}
		var id int64
		if err := db.QueryRow(t.Context(), `INSERT INTO users (role, username, password_hash, local_password_login_enabled, break_glass, enabled)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, role, name, hash, password, breakGlass, enabled).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	passwordOnly := account("password-only", "user", true, false, true)
	linked := account("password-and-provider", "user", true, false, true)
	// A directory provider signs people in; an OAuth one can't without a
	// public URL (the restored database has none); a network one signs in
	// only from a device on its network.
	provider := func(id int, modes string) {
		dbExec(t, db, `INSERT INTO plugin_installations (id, plugin_id, version, install_path, enabled) VALUES ($1, $2, '1.0.0', '/plugins/auth', true)`, id, fmt.Sprintf("silo.auth.%d", id))
		dbExec(t, db, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
			VALUES ($1, 'auth_provider.v1', 'sign-in', jsonb_build_object('auth_modes', $2::jsonb))`, id, modes)
		dbExec(t, db, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'sign-in', true)`, id)
	}
	provider(9101, `["password"]`)
	provider(9102, `["oauth2"]`)
	provider(9103, `["network"]`)
	dbExec(t, db, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES (9101, 'subject', $1)`, linked)
	oauthOnly := account("password-and-oauth", "user", true, false, true)
	dbExec(t, db, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES (9102, 'subject', $1)`, oauthOnly)
	networkOnly := account("password-and-network", "user", true, false, true)
	dbExec(t, db, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES (9103, 'subject', $1)`, networkOnly)
	account("break-glass", "admin", true, true, true)
	account("disabled", "user", true, false, false)
	keyHolder := account("key-holder", "user", false, false, true)
	insertDeviceKey(t, db, keyHolder, appkeystest.NewKey(t), deviceKeyRow{})
	glassKeys := account("break-glass-keys", "admin", false, true, true)
	insertDeviceKey(t, db, glassKeys, appkeystest.NewKey(t), deviceKeyRow{})

	got, remedy, err := PGLoader{}.StrandedAccounts(t.Context(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	// Local passwords are off and no break-glass administrator is listed,
	// so a password would not help anyone named.
	if remedy != "Link a sign-in provider account for each" {
		t.Errorf("remedy = %q", remedy)
	}
	// The key holder's local key needs local passwords, which are off; the
	// break-glass admin's key works, so it has a way in.
	want := []string{fmt.Sprintf("password-only (account %d)", passwordOnly), fmt.Sprintf("password-and-oauth (account %d)", oauthOnly),
		fmt.Sprintf("password-and-network (account %d)", networkOnly), fmt.Sprintf("key-holder (account %d)", keyHolder)}
	if !slices.Equal(got, want) {
		t.Fatalf("stranded = %q, want %q", got, want)
	}
}

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
// is listed, and is not once it has a provider account it can sign in with;
// a break-glass administrator, a disabled account and a key holder whose
// key still works are not.
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
	dbExec(t, db, `INSERT INTO plugin_installations (id, plugin_id, version, install_path, enabled) VALUES (9101, 'silo.auth.oidc', '1.0.0', '/plugins/oidc', true)`)
	dbExec(t, db, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES (9101, 'oidc', true)`)
	dbExec(t, db, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES (9101, 'subject', $1)`, linked)
	account("break-glass", "admin", true, true, true)
	account("disabled", "user", true, false, false)
	keyHolder := account("key-holder", "user", false, false, true)
	insertDeviceKey(t, db, keyHolder, appkeystest.NewKey(t), deviceKeyRow{})
	glassKeys := account("break-glass-keys", "admin", false, true, true)
	insertDeviceKey(t, db, glassKeys, appkeystest.NewKey(t), deviceKeyRow{})

	got, err := PGLoader{}.StrandedAccounts(t.Context(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	// The key holder's local key needs local passwords, which are off; the
	// break-glass admin's key works, so it has a way in.
	want := []string{fmt.Sprintf("password-only (account %d)", passwordOnly), fmt.Sprintf("key-holder (account %d)", keyHolder)}
	if !slices.Equal(got, want) {
		t.Fatalf("stranded = %q, want %q", got, want)
	}
}

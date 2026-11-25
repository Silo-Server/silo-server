package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Turning local password sign-in off needs a break-glass admin and an
// OIDC or LDAP sign-in provider that can sign people in, through the batch
// and the single-key settings writes alike. Without the provider check the
// server accepted the change with nobody else able to sign in.
func TestLocalLoginSettingRequiresBreakGlassDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var existing int
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM users WHERE break_glass`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing > 0 {
		t.Skipf("%d break-glass accounts already in the database", existing)
	}

	key := config.AuthLocalPasswordLoginSettingKey
	store := &fakeServerSettingsStore{values: map[string]string{}}
	h := &AdminHandler{SettingsRepo: store, pool: pool}
	for _, single := range []bool{false, true} {
		requireAdminError(t, saveAdminSetting(t, h, key, "false", single), 409, "break_glass_required")
	}
	if _, stored := store.values[key]; stored {
		t.Fatalf("refused write stored %q", store.values[key])
	}
	// Without account storage the change cannot be checked, so it is refused.
	if err := saveAdminSetting(t, &AdminHandler{SettingsRepo: store}, key, "false", true); err == nil {
		t.Fatal("unchecked write accepted")
	}

	suffix := fmt.Sprint(time.Now().UnixNano())
	var id int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled, break_glass)
		VALUES ($1, $2, 'x', 'admin', true, true) RETURNING id`, "glass-"+suffix, "glass-"+suffix+"@example.invalid").Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })

	var providers int
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM plugin_auth_bindings WHERE enabled`).Scan(&providers); err != nil {
		t.Fatal(err)
	}
	if providers > 0 {
		t.Skipf("%d enabled sign-in bindings already in the database", providers)
	}
	for _, single := range []bool{false, true} {
		requireAdminError(t, saveAdminSetting(t, h, key, "false", single), 409, "sign_in_provider_required")
	}
	if _, stored := store.values[key]; stored {
		t.Fatalf("refused write stored %q", store.values[key])
	}

	var installation int
	if err := pool.QueryRow(t.Context(), `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/local-login-setting-test', true, 'manual', 'plugin') RETURNING id`, "local-login-"+suffix).Scan(&installation); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installation)
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'oidc', '{"auth_modes":["oauth2"]}'),
		       ($1, 'auth_provider.v1', 'ldap', '{"auth_modes":["password"]}')`, installation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled)
		VALUES ($1, 'oidc', false), ($1, 'ldap', false)`, installation); err != nil {
		t.Fatal(err)
	}

	// An OpenID Connect provider signs nobody in without a public URL, so it
	// does not count until one is set; a directory provider needs none.
	for _, tc := range []struct {
		name, binding, publicURL string
		accepted                 bool
	}{
		{"oauth without a public URL", "oidc", "", false},
		{"oauth with a public URL", "oidc", "https://silo.example.test", true},
		{"directory without a public URL", "ldap", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), `UPDATE plugin_auth_bindings SET enabled = (capability_id = $2) WHERE plugin_installation_id = $1`,
				installation, tc.binding); err != nil {
				t.Fatal(err)
			}
			delete(store.values, key)
			store.values[config.ServerPublicURLSettingKey] = tc.publicURL
			err := saveAdminSetting(t, h, key, "false", true)
			if !tc.accepted {
				requireAdminError(t, err, 409, "sign_in_provider_required")
				if _, stored := store.values[key]; stored {
					t.Fatalf("refused write stored %q", store.values[key])
				}
				return
			}
			if err != nil || store.values[key] != "false" {
				t.Fatalf("accepted write: %v, stored %v", err, store.values)
			}
		})
	}

	// With password sign-in off and only the OpenID Connect provider on,
	// clearing the public URL would leave nobody but break-glass admins able
	// to sign in.
	if _, err := pool.Exec(t.Context(), `UPDATE plugin_auth_bindings SET enabled = (capability_id = 'oidc') WHERE plugin_installation_id = $1`, installation); err != nil {
		t.Fatal(err)
	}
	store.values[key] = "false"
	store.values[config.ServerPublicURLSettingKey] = "https://silo.example.test"
	requireAdminError(t, saveAdminSetting(t, h, config.ServerPublicURLSettingKey, "", true), 409, "sign_in_provider_required")
	if store.values[config.ServerPublicURLSettingKey] == "" {
		t.Fatal("refused write cleared the public URL")
	}
}

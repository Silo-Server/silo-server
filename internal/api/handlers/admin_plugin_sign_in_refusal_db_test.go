package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// TestPluginDisableAndUninstallRefusedBeforeStopDB: with password sign-in
// off, disabling or uninstalling the last provider people can sign in with
// is refused before the plugin is stopped. Without the check up front, the
// plugin stops, the write's own check refuses, and the handler restarts it:
// sign-in drops for everyone meanwhile.
func TestPluginDisableAndUninstallRefusedBeforeStopDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var others int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_bindings WHERE enabled`).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others > 0 {
		t.Skipf("%d enabled sign-in bindings already in the database", others)
	}
	for _, key := range []string{config.AuthLocalPasswordLoginSettingKey, config.ServerPublicURLSettingKey} {
		var saved *string
		_ = pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&saved)
		t.Cleanup(func() {
			if saved == nil {
				_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, key)
				return
			}
			_, _ = pool.Exec(context.Background(), `UPDATE server_settings SET value = $2 WHERE key = $1`, key, *saved)
		})
	}
	set := func(key, value string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	set(config.ServerPublicURLSettingKey, "https://silo.example.test")
	set(config.AuthLocalPasswordLoginSettingKey, "false")

	var id int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/refused-before-stop', true, 'manual', 'plugin') RETURNING id`,
		fmt.Sprintf("refused-before-stop-%d", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id) })
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'oidc', '{"auth_modes":["oauth2"]}')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'oidc', true)`, id); err != nil {
		t.Fatal(err)
	}

	var stopped []int
	h := &PluginHandler{
		installations: plugins.NewInstallationStore(pool),
		service:       &plugins.Service{},
		stopForTest: func(installationID int) error {
			stopped = append(stopped, installationID)
			return nil
		},
		startForTest: func(int) error { return nil },
	}
	if _, err := h.UpdateAdminPluginInstallation(ctx, id, PluginInstallationUpdateInput{Enabled: new(false)}); !errors.Is(err, auth.ErrSignInProviderRequired) {
		t.Fatalf("disable = %v, want ErrSignInProviderRequired", err)
	}
	if err := h.DeleteAdminPluginInstallation(ctx, id); !errors.Is(err, auth.ErrSignInProviderRequired) {
		t.Fatalf("uninstall = %v, want ErrSignInProviderRequired", err)
	}
	if len(stopped) != 0 {
		t.Fatalf("refused changes stopped installations %v", stopped)
	}
}

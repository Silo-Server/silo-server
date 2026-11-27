package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// TestProvisionAppliesDeclarationThatLeavesNoSignInProviderDB: provisioning
// applies the operator's declared configuration at startup. A declaration
// that leaves password sign-in off with no provider people can sign in with
// is applied, not refused (refusing would leave the server unable to
// converge on its own configuration). The provisioning status of the
// declared sign-in plugin says so while it is true, and stops as soon as
// password sign-in is back on (a break-glass admin's save and the recovery
// command both write the setting) or a provider is usable. The auth
// package's rule is stood in for: sign-in works while the setting is on or
// the declared binding is on.
func TestProvisionAppliesDeclarationThatLeavesNoSignInProviderDB(t *testing.T) {
	previous := signInCheck
	t.Cleanup(func() { RegisterSignInCheck(previous) })
	ctx := t.Context()
	writer, installation := newConfigProvisionWriter(t, ProvisionPlugin{
		Version:     "1.2.3",
		Config:      map[string]map[string]any{"connection": {"label": "saved"}},
		AuthBinding: &ProvisionAuthBinding{CapabilityID: "login", Enabled: new(false)},
	})
	checks := 0
	RegisterSignInCheck(func(ctx context.Context, db SignInQuerier) (bool, error) {
		checks++
		var works bool
		err := db.QueryRow(ctx, `SELECT COALESCE((SELECT value = 'true' FROM server_settings WHERE key = $1), true)
			OR COALESCE((SELECT bool_or(enabled) FROM plugin_auth_bindings WHERE plugin_installation_id = $2), false)`,
			config.AuthLocalPasswordLoginSettingKey, installation.ID).Scan(&works)
		return works, err
	})
	setting := func(value string) {
		t.Helper()
		if _, err := writer.pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthLocalPasswordLoginSettingKey, value); err != nil {
			t.Fatal(err)
		}
	}
	var saved *string
	_ = writer.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey).Scan(&saved)
	t.Cleanup(func() {
		if saved == nil {
			_, _ = writer.pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey)
			return
		}
		_, _ = writer.pool.Exec(context.Background(), `UPDATE server_settings SET value = $2 WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey, *saved)
	})
	detail := func() string {
		t.Helper()
		statuses, err := ListProvisionStatus(ctx, writer.pool)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			if status.PluginID == tmdbID {
				return status.Detail
			}
		}
		t.Fatal("no status for the declared plugin")
		return ""
	}
	setting("false")

	if err := writer.StartOffline(ctx); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	if checks == 0 {
		t.Fatal("provisioning did not check for a sign-in provider")
	}
	binding, err := writer.configs.GetAuthBinding(ctx, installation.ID, "login")
	if err != nil || binding.Enabled {
		t.Fatalf("declared binding not applied: %+v, %v", binding, err)
	}
	if got := detail(); got != SignInProviderWarning {
		t.Fatalf("status detail = %q, want the warning", got)
	}
	// The warning names every way back, starting with the declaration.
	for _, way := range []string{"SILO_CONFIG or SILO_PLUGINS", "/login?local=1", "silo auth local-login enable"} {
		if !strings.Contains(SignInProviderWarning, way) {
			t.Errorf("warning does not name %q", way)
		}
	}

	// Password sign-in turned back on ends it at once, with no new apply.
	setting("true")
	if got := detail(); got != "" {
		t.Fatalf("after password sign-in is back on: detail = %q", got)
	}
	// So does a provider that can sign people in.
	setting("false")
	if got := detail(); got != SignInProviderWarning {
		t.Fatalf("with password sign-in off again: detail = %q", got)
	}
	if _, err := writer.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, installation.ID); err != nil {
		t.Fatal(err)
	}
	if got := detail(); got != "" {
		t.Fatalf("with the provider on: detail = %q", got)
	}
}

// TestSignInWarningOnlyOnPluginsThatCanBeTheProviderDB: the warning asks for
// the sign-in provider back, so only a plugin with an OIDC or LDAP binding
// carries it; a plugin that only provides network identity never can be
// that provider.
func TestSignInWarningOnlyOnPluginsThatCanBeTheProviderDB(t *testing.T) {
	previous := signInCheck
	t.Cleanup(func() { RegisterSignInCheck(previous) })
	RegisterSignInCheck(func(context.Context, SignInQuerier) (bool, error) { return false, nil })
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	suffix := strings.ReplaceAll(t.Name(), "/", "-")
	install := func(plugin, capability, modes string) {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
			VALUES ($1, '0', '/nonexistent/'||$1, true, 'manual', 'plugin') RETURNING id`, plugin).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id) })
		if _, err := pool.Exec(ctx, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
			VALUES ($1, 'auth_provider.v1', $2, jsonb_build_object('auth_modes', jsonb_build_array($3::text)))`, id, capability, modes); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, $2, false)`, id, capability); err != nil {
			t.Fatal(err)
		}
	}
	signIn, network := "sign-in-"+suffix, "network-"+suffix
	install(signIn, "oidc", "oauth2")
	install(network, "tailnet", "network")

	statuses, err := withSignInWarning(ctx, pool, []ProvisionStatus{
		{PluginID: signIn, State: "applied"}, {PluginID: network, State: "applied"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].Detail != SignInProviderWarning {
		t.Errorf("sign-in plugin detail = %q, want the warning", statuses[0].Detail)
	}
	if statuses[1].Detail != "" {
		t.Errorf("network identity plugin detail = %q, want none", statuses[1].Detail)
	}
}

// TestStoredSignInWarningIsWorkedOutAgainDB: a status an earlier build stored
// with the warning does not keep it once sign-in works again.
func TestStoredSignInWarningIsWorkedOutAgainDB(t *testing.T) {
	previous := signInCheck
	t.Cleanup(func() { RegisterSignInCheck(previous) })
	RegisterSignInCheck(func(context.Context, SignInQuerier) (bool, error) { return true, nil })
	statuses, err := withSignInWarning(t.Context(), builtinGuardTestPool(t), []ProvisionStatus{
		{PluginID: "stored-warning", State: "applied", Detail: "Password sign-in is off and no sign-in provider can sign people in, so only break-glass administrators can sign in. Turn password sign-in back on or enable a sign-in provider."},
		{PluginID: "other", State: "refused", Detail: "The sign-in binding conflicts with another active binding."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].Detail != "" {
		t.Errorf("stored warning kept: %q", statuses[0].Detail)
	}
	if statuses[1].Detail == "" {
		t.Error("an unrelated detail was dropped")
	}
}

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// signInGuardEnv is one provider people can sign in with (an OpenID
// Connect binding with a public URL set), with no other enabled OIDC or
// LDAP binding in the database.
type signInGuardEnv struct {
	*externalSignInEnv
	oidc     int
	bindings *plugins.RuntimeConfigStore
	installs *plugins.InstallationStore
}

func newSignInGuardEnv(t *testing.T) *signInGuardEnv {
	t.Helper()
	env := newExternalSignInEnv(t)
	t.Cleanup(saveSettings(t, env.pool, config.ServerPublicURLSettingKey))
	var others int
	if err := env.pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM plugin_auth_bindings b
		JOIN plugin_installations p ON p.id = b.plugin_installation_id AND p.enabled
		WHERE `+primarySignInBindingSQL("b")).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others > 0 {
		t.Skipf("%d enabled sign-in bindings already in the database", others)
	}
	env.setSetting(t, config.ServerPublicURLSettingKey, "https://silo.example.test")
	return &signInGuardEnv{
		externalSignInEnv: env,
		oidc:              env.authInstallation(t, "guard-oidc", "oidc", "oauth2", true),
		bindings:          plugins.NewRuntimeConfigStore(env.pool, nil),
		installs:          plugins.NewInstallationStore(env.pool),
	}
}

// TestSignInProviderGuardDB pins the reverse door: while password sign-in is
// off, no write may take away the last provider people can sign in with,
// since only break-glass admins could sign in after it. Each write is
// allowed with passwords on, or while another provider remains.
func TestSignInProviderGuardDB(t *testing.T) {
	writes := []struct {
		name  string
		apply func(e *signInGuardEnv) error
	}{
		{"disable the binding", func(e *signInGuardEnv) error {
			return e.bindings.UpsertAuthBinding(context.Background(), plugins.AuthBinding{InstallationID: e.oidc, CapabilityID: "oidc", Enabled: false})
		}},
		{"disable the installation", func(e *signInGuardEnv) error {
			return e.installs.Update(context.Background(), e.oidc, plugins.UpdateInstallationInput{Enabled: new(false)})
		}},
		{"check before uninstalling", func(e *signInGuardEnv) error {
			return e.installs.CheckSignInProviderRemoval(context.Background(), e.oidc)
		}},
		{"uninstall", func(e *signInGuardEnv) error {
			return e.installs.Delete(context.Background(), e.oidc)
		}},
		{"clear the public URL", func(e *signInGuardEnv) error {
			local, err := readServerSetting(context.Background(), e.pool, config.AuthLocalPasswordLoginSettingKey)
			if err != nil {
				return err
			}
			return CheckLocalLoginSettingChange(context.Background(), e.pool,
				LocalLoginSettings{LocalPasswordLogin: local, PublicURL: "https://silo.example.test"},
				LocalLoginSettings{LocalPasswordLogin: local})
		}},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			t.Run("refused with passwords off", func(t *testing.T) {
				e := newSignInGuardEnv(t)
				e.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
				if err := w.apply(e); !errors.Is(err, ErrSignInProviderRequired) {
					t.Fatalf("err = %v, want ErrSignInProviderRequired", err)
				}
				var bindingOn, installed bool
				if err := e.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM plugin_installations p
					JOIN plugin_auth_bindings b ON b.plugin_installation_id = p.id AND b.enabled
					WHERE p.id = $1 AND p.enabled), EXISTS (SELECT 1 FROM plugin_installations WHERE id = $1)`,
					e.oidc).Scan(&bindingOn, &installed); err != nil {
					t.Fatal(err)
				}
				if !bindingOn || !installed {
					t.Fatalf("refused write changed the provider: binding on %v, installed %v", bindingOn, installed)
				}
			})
			t.Run("allowed with passwords on", func(t *testing.T) {
				e := newSignInGuardEnv(t)
				e.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
				if err := w.apply(e); err != nil {
					t.Fatalf("err = %v", err)
				}
			})
			t.Run("allowed while a directory provider remains", func(t *testing.T) {
				e := newSignInGuardEnv(t)
				e.authInstallation(t, "guard-ldap", "ldap", "password", true)
				e.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
				if err := w.apply(e); err != nil {
					t.Fatalf("err = %v", err)
				}
			})
		})
	}
}

// TestSignInProviderGuardSerializesWithSettingsSaveDB: a save turning
// passwords off and a write disabling the last provider cannot both pass.
// The save holds the settings lock until it commits; the binding write waits
// for it, then sees passwords off and is refused.
func TestSignInProviderGuardSerializesWithSettingsSaveDB(t *testing.T) {
	e := newSignInGuardEnv(t)
	ctx := t.Context()
	glass := e.localAccount(t, "guard-glass", models.RoleAdmin)
	e.exec(t, `UPDATE users SET break_glass = true WHERE id = $1`, glass.ID)
	e.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	save, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = save.Rollback(context.Background()) }()
	if err := lockServerSettings(ctx, save); err != nil {
		t.Fatal(err)
	}
	if err := CheckLocalLoginSettingChange(ctx, save,
		LocalLoginSettings{LocalPasswordLogin: "true", PublicURL: "https://silo.example.test"},
		LocalLoginSettings{LocalPasswordLogin: "false", PublicURL: "https://silo.example.test"}); err != nil {
		t.Fatalf("save check = %v", err)
	}
	if _, err := save.Exec(ctx, `UPDATE server_settings SET value = 'false' WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		result <- e.bindings.UpsertAuthBinding(context.Background(), plugins.AuthBinding{InstallationID: e.oidc, CapabilityID: "oidc", Enabled: false})
	}()
	// Wait until the binding write is blocked on the settings lock.
	for {
		var waiting bool
		if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
			AND objid = (hashtextextended($1, 0) & 4294967295)::oid)`, config.ServerSettingsMutationLock).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("binding write finished while the save held the lock: %v", err)
		default:
		}
	}
	if err := save.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrSignInProviderRequired) {
		t.Fatalf("binding write after the save = %v, want ErrSignInProviderRequired", err)
	}
	var on bool
	if err := e.pool.QueryRow(ctx, `SELECT enabled FROM plugin_auth_bindings WHERE plugin_installation_id = $1`, e.oidc).Scan(&on); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("the last provider was turned off after passwords went off")
	}
}

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

// TestServerWithoutUsableProviderCanStillBeRepairedAndManagedDB: a server
// already left with password sign-in off and no provider people can use (by
// a provisioned declaration, a restore, or a switch-off from before the check
// existed) can still be repaired and manage its plugins. The guard refuses
// only a write that takes the last usable provider away; enabling a disabled
// sign-in plugin (the way back from the admin page) is allowed, and so is
// every change to a plugin without a sign-in binding.
func TestServerWithoutUsableProviderCanStillBeRepairedAndManagedDB(t *testing.T) {
	broken := func(t *testing.T) *signInGuardEnv {
		e := newSignInGuardEnv(t)
		// The OpenID Connect provider signs nobody in without a public URL.
		e.setSetting(t, config.ServerPublicURLSettingKey, "")
		e.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
		return e
	}
	plain := func(t *testing.T, e *signInGuardEnv, enabled bool) int {
		t.Helper()
		var id int
		if err := e.pool.QueryRow(t.Context(), `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
			VALUES ($1, '0', '/nonexistent/'||$1, $2, 'manual', 'plugin') RETURNING id`, e.name("plain"), enabled).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id) })
		return id
	}
	ctx := context.Background()
	for name, write := range map[string]func(t *testing.T, e *signInGuardEnv) error{
		"enable a plugin without sign-in": func(t *testing.T, e *signInGuardEnv) error {
			return e.installs.Update(ctx, plain(t, e, false), plugins.UpdateInstallationInput{Enabled: new(true)})
		},
		"disable a plugin without sign-in": func(t *testing.T, e *signInGuardEnv) error {
			return e.installs.Update(ctx, plain(t, e, true), plugins.UpdateInstallationInput{Enabled: new(false)})
		},
		"uninstall a plugin without sign-in": func(t *testing.T, e *signInGuardEnv) error {
			return e.installs.Delete(ctx, plain(t, e, true))
		},
		"update the release of a plugin without sign-in": func(t *testing.T, e *signInGuardEnv) error {
			return e.installs.Update(ctx, plain(t, e, true), plugins.UpdateInstallationInput{Capabilities: []plugins.Capability{
				{Type: "metadata_provider.v1", ID: "meta", Metadata: map[string]any{}},
			}})
		},
		"set the public URL": func(t *testing.T, e *signInGuardEnv) error {
			return CheckLocalLoginSettingChange(ctx, e.pool,
				LocalLoginSettings{LocalPasswordLogin: "false"},
				LocalLoginSettings{LocalPasswordLogin: "false", PublicURL: "https://silo.example.test"})
		},
		"enable a disabled sign-in plugin": func(t *testing.T, e *signInGuardEnv) error {
			e.exec(t, `UPDATE plugin_auth_bindings SET enabled = false WHERE plugin_installation_id = $1`, e.oidc)
			ldap := e.authInstallation(t, "guard-ldap", "ldap", "password", true)
			e.exec(t, `UPDATE plugin_installations SET enabled = false WHERE id = $1`, ldap)
			return e.installs.Update(ctx, ldap, plugins.UpdateInstallationInput{Enabled: new(true)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := write(t, broken(t)); err != nil {
				t.Fatalf("refused from the broken state: %v", err)
			}
		})
	}
}

// TestBreakGlassWithoutPasswordIsNotCountedDB: a break-glass admin counts
// only with a password that signs in (models.User.HasLocalPassword), so
// password sign-in cannot be turned off on the strength of an account with
// its password sign-in on but no password stored. Every account on main has
// a stored hash, so the row is written with SQL; the rule keeps the count
// true once accounts without a password exist.
func TestBreakGlassWithoutPasswordIsNotCountedDB(t *testing.T) {
	e := newSignInGuardEnv(t)
	ctx := t.Context()
	if count, err := usableBreakGlassCount(ctx, e.pool, 0); err != nil || count != 0 {
		t.Skipf("%d usable break-glass accounts already in the database (%v)", count, err)
	}
	glass := e.localAccount(t, "glass-no-password", models.RoleAdmin)
	e.exec(t, `UPDATE users SET break_glass = true, local_password_login_enabled = true, password_hash = NULL WHERE id = $1`, glass.ID)
	stored, err := NewUserRepository(e.pool).GetByID(ctx, glass.ID)
	if err != nil {
		t.Fatal(err)
	}
	if usableBreakGlass(stored) {
		t.Error("an account with no stored password counts as a usable break-glass admin")
	}
	if count, err := usableBreakGlassCount(ctx, e.pool, 0); err != nil || count != 0 {
		t.Fatalf("usable break-glass count = %d, %v; want 0", count, err)
	}
	err = CheckLocalLoginSettingChange(ctx, e.pool,
		LocalLoginSettings{LocalPasswordLogin: "true", PublicURL: "https://silo.example.test"},
		LocalLoginSettings{LocalPasswordLogin: "false", PublicURL: "https://silo.example.test"})
	if !errors.Is(err, ErrBreakGlassRequired) {
		t.Fatalf("turning password sign-in off = %v, want ErrBreakGlassRequired", err)
	}
}

// TestUnlinkNeedsAPasswordThatSignsInDB: an account may disconnect its only
// identity only while its password still signs in, by the same definition
// the break-glass count uses (models.User.HasLocalPassword): password
// sign-in on for the account is not enough without a stored password. Every
// account on main has a stored hash, so the row is written with SQL.
func TestUnlinkNeedsAPasswordThatSignsInDB(t *testing.T) {
	e := newSignInGuardEnv(t)
	ctx := t.Context()
	// Password sign-in on for the server, so only the missing hash refuses.
	e.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	account := e.localAccount(t, "unlink-no-password", models.RoleUser)
	link, err := insertIdentity(ctx, e.pool, account.ID, e.oidc, e.identity("unlink-no-password-idp"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE users SET local_password_login_enabled = true, password_hash = NULL WHERE id = $1`, account.ID)
	svc := NewIdentityService(e.pool)
	if can, err := svc.CanUnlinkOwn(ctx, account.ID); err != nil || can {
		t.Fatalf("can unlink with no stored password = %v, %v", can, err)
	}
	if err := svc.UnlinkOwn(ctx, account.ID, link.ID); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("unlink with no stored password = %v, want ErrLastSignInMethod", err)
	}
}

package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// externalSignInEnv is one test's slice of the migrated database named by
// SILO_TEST_DATABASE_URL: an auth plugin installation with a binding, a
// resolver that creates profiles in Postgres, and a suffix every account
// name of the test carries so cleanup finds them.
type externalSignInEnv struct {
	pool           *pgxpool.Pool
	resolver       *AccountResolver
	installationID int
	suffix         string
	revoked        []int
}

func newExternalSignInEnv(t *testing.T) *externalSignInEnv {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	env := &externalSignInEnv{pool: pool, suffix: fmt.Sprintf("x%d", time.Now().UnixNano())}
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/external-sign-in-test', true, 'manual', 'plugin') RETURNING id`,
		"external-sign-in-"+env.suffix).Scan(&env.installationID); err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'oidc', false)`,
		env.installationID); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	saved := saveSettings(t, pool, config.AuthLocalPasswordLoginSettingKey, config.AuthEmailAutoMatchSettingKey)
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		saved()
		_, _ = pool.Exec(cleanup, `DELETE FROM plugin_installations WHERE id = $1`, env.installationID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE username LIKE $1 OR email LIKE $1`, "%"+env.suffix+"%")
	})
	env.resolver = NewAccountResolver(pool, NewAccountProvisioner(NewUserRepository(pool), pgstore.NewPostgresProvider(pool)),
		func(_ context.Context, userID int) { env.revoked = append(env.revoked, userID) })
	return env
}

// usableBreakGlass counts the enabled break-glass admins that can still use
// a local password, across the whole test database.
func (e *externalSignInEnv) usableBreakGlass(t *testing.T) int {
	t.Helper()
	var count int
	if err := e.pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM users
		WHERE break_glass AND role = 'admin' AND enabled AND local_password_login_enabled`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// saveSettings returns a function that restores the named server settings.
func saveSettings(t *testing.T, pool *pgxpool.Pool, keys ...string) func() {
	t.Helper()
	values := map[string]*string{}
	for _, key := range keys {
		var value string
		err := pool.QueryRow(t.Context(), `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			values[key] = nil
		case err != nil:
			t.Fatalf("read setting %s: %v", key, err)
		default:
			values[key] = &value
		}
	}
	return func() {
		ctx := context.Background()
		for key, value := range values {
			if value == nil {
				_, _ = pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, key)
				continue
			}
			_, _ = pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, *value)
		}
	}
}

func (e *externalSignInEnv) setSetting(t *testing.T, key, value string) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO server_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func (e *externalSignInEnv) name(label string) string { return label + "-" + e.suffix }

func (e *externalSignInEnv) identity(label string) ExternalIdentity {
	return ExternalIdentity{
		Subject:     "https://id.example.test|" + e.name(label),
		Issuer:      "https://id.example.test",
		Username:    e.name(label),
		Email:       e.name(label) + "@example.test",
		DisplayName: "Person " + label,
	}
}

// localAccount creates a local-password account the test owns.
func (e *externalSignInEnv) localAccount(t *testing.T, label, role string) *models.User {
	t.Helper()
	user, err := NewUserRepository(e.pool).Create(t.Context(), models.CreateUserInput{
		Username: e.name(label), Email: e.name(label) + "@example.test", Password: "correct horse battery", Role: role,
	})
	if err != nil {
		t.Fatalf("create %s: %v", label, err)
	}
	return user
}

func (e *externalSignInEnv) resolve(t *testing.T, identity ExternalIdentity, autoProvision bool, linking int) (*models.User, error) {
	t.Helper()
	user, _, err := e.resolver.Resolve(t.Context(), ResolveInput{InstallationID: e.installationID, AutoProvision: autoProvision, Identity: identity, LinkingUserID: linking})
	return user, err
}

func (e *externalSignInEnv) identityRow(t *testing.T, subject string) *LinkedIdentity {
	t.Helper()
	identity, err := identityBySubject(t.Context(), e.pool, e.installationID, subject)
	if err != nil {
		t.Fatalf("identity %s: %v", subject, err)
	}
	return identity
}

func TestExternalSignInCreatesAccountWithProfileDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	identity := env.identity("alice")
	user, err := env.resolve(t, identity, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != env.name("alice") || user.Email != env.name("alice")+"@example.test" {
		t.Fatalf("account = %s / %s", user.Username, user.Email)
	}
	if user.Role != models.RoleUser || user.LocalPasswordLoginEnabled || user.AccessGroupID == nil {
		t.Fatalf("role %s, local password %v, group %v", user.Role, user.LocalPasswordLoginEnabled, user.AccessGroupID)
	}
	store, err := pgstore.NewPostgresProvider(env.pool).ForUser(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := store.ListProfiles(t.Context())
	if err != nil || len(profiles) != 1 || profiles[0].Name != "Person alice" {
		t.Fatalf("profiles = %+v, %v", profiles, err)
	}
	row := env.identityRow(t, identity.Subject)
	if row.UserID != user.ID || row.Username != identity.Username || row.Issuer != identity.Issuer || row.LastSignInAt == nil {
		t.Fatalf("identity row = %+v", row)
	}

	// The same person signs in again: same account, details refreshed.
	identity.Email = env.name("alice-new") + "@example.test"
	again, err := env.resolve(t, identity, true, 0)
	if err != nil || again.ID != user.ID {
		t.Fatalf("second sign-in = %+v, %v", again, err)
	}
	if row := env.identityRow(t, identity.Subject); row.Email != identity.Email {
		t.Fatalf("identity email = %q", row.Email)
	}
}

func TestExternalSignInRefusalsDB(t *testing.T) {
	env := newExternalSignInEnv(t)

	t.Run("creation off", func(t *testing.T) {
		// The provider admitted the person; only the missing account
		// refuses them, which is its own reason (still a not_permitted to
		// the frozen v1 surface).
		_, err := env.resolve(t, env.identity("nobody"), false, 0)
		if !errors.Is(err, ErrAccountRequired) || !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("err = %v, want ErrAccountRequired", err)
		}
	})
	t.Run("email held by an unlinked account", func(t *testing.T) {
		local := env.localAccount(t, "bob", models.RoleUser)
		identity := env.identity("bob-idp")
		identity.Email = local.Email
		if _, err := env.resolve(t, identity, true, 0); !errors.Is(err, ErrEmailInUse) {
			t.Fatalf("err = %v, want ErrEmailInUse", err)
		}
		var count int
		_ = env.pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM users WHERE username LIKE $1`, "bob-idp%").Scan(&count)
		if count != 0 {
			t.Fatalf("a duplicate account was created")
		}
	})
	t.Run("disabled account", func(t *testing.T) {
		identity := env.identity("carol")
		user, err := env.resolve(t, identity, true, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.pool.Exec(t.Context(), `UPDATE users SET enabled = false WHERE id = $1`, user.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := env.resolve(t, identity, true, 0); !errors.Is(err, ErrUserDisabled) {
			t.Fatalf("err = %v, want ErrUserDisabled", err)
		}
	})
}

func TestExternalSignInEmailAutoMatchDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	yes, no := true, false
	for _, tc := range []struct {
		name      string
		autoMatch string
		verified  *bool
		linked    bool
	}{
		{"off", "false", &yes, false},
		{"on but unverified", "true", &no, false},
		{"on but not stated", "true", nil, false},
		{"on and verified", "true", &yes, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.setSetting(t, config.AuthEmailAutoMatchSettingKey, tc.autoMatch)
			local := env.localAccount(t, "match-"+strings.ReplaceAll(tc.name, " ", "-"), models.RoleUser)
			identity := env.identity("idp-" + strings.ReplaceAll(tc.name, " ", "-"))
			identity.Email = local.Email
			identity.EmailVerified = tc.verified
			user, err := env.resolve(t, identity, true, 0)
			if !tc.linked {
				if !errors.Is(err, ErrEmailInUse) {
					t.Fatalf("err = %v, want ErrEmailInUse", err)
				}
				return
			}
			if err != nil || user.ID != local.ID {
				t.Fatalf("resolved %+v, %v; want account %d", user, err, local.ID)
			}
			if user.LocalPasswordLoginEnabled {
				t.Fatal("linking kept the local password on")
			}
		})
	}

	// A verified email never hands the provider an admin or a break-glass
	// account (the Owner is an admin): those are refused like any other
	// taken email, and stay unlinked.
	env.setSetting(t, config.AuthEmailAutoMatchSettingKey, "true")
	admin := env.localAccount(t, "match-admin", models.RoleAdmin)
	glass := env.localAccount(t, "match-glass", models.RoleAdmin)
	if _, err := env.pool.Exec(t.Context(), `UPDATE users SET break_glass = true WHERE id = $1`, glass.ID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []*models.User{admin, glass} {
		identity := env.identity("idp-" + target.Username)
		identity.Email = target.Email
		identity.EmailVerified = &yes
		if _, err := env.resolve(t, identity, true, 0); !errors.Is(err, ErrEmailInUse) {
			t.Fatalf("%s: err = %v, want ErrEmailInUse", target.Username, err)
		}
		if after, _ := NewUserRepository(env.pool).GetByID(t.Context(), target.ID); !after.LocalPasswordLoginEnabled {
			t.Fatalf("%s lost its local password", target.Username)
		}
	}
}

func TestExternalSignInLinkingFlowDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	local := env.localAccount(t, "dave", models.RoleUser)
	identity := env.identity("dave-idp")
	user, err := env.resolve(t, identity, false, local.ID)
	if err != nil || user.ID != local.ID {
		t.Fatalf("link = %+v, %v", user, err)
	}
	if user.LocalPasswordLoginEnabled {
		t.Fatal("linking kept the local password on")
	}
	// The identity cannot move to another account.
	other := env.localAccount(t, "erin", models.RoleUser)
	if _, err := env.resolve(t, identity, false, other.ID); !errors.Is(err, ErrIdentityLinkedElsewhere) {
		t.Fatalf("err = %v, want ErrIdentityLinkedElsewhere", err)
	}
	// An account holds one identity per installation.
	if _, err := env.resolve(t, env.identity("dave-second"), false, local.ID); !errors.Is(err, ErrAccountAlreadyLinked) {
		t.Fatalf("err = %v, want ErrAccountAlreadyLinked", err)
	}
	// Linking the identity the account already holds is refused the same way.
	if _, err := env.resolve(t, identity, false, local.ID); !errors.Is(err, ErrAccountAlreadyLinked) {
		t.Fatalf("relink: err = %v, want ErrAccountAlreadyLinked", err)
	}
	// A break-glass admin keeps its local password when linked.
	admin := env.localAccount(t, "glass", models.RoleAdmin)
	if _, err := env.pool.Exec(t.Context(), `UPDATE users SET break_glass = true WHERE id = $1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	linked, err := env.resolve(t, env.identity("glass-idp"), false, admin.ID)
	if err != nil || !linked.LocalPasswordLoginEnabled {
		t.Fatalf("break-glass link = %+v, %v", linked, err)
	}
}

func TestExternalSignInUsernameCollisionDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	env.localAccount(t, "frank", models.RoleUser)
	identity := env.identity("frank-idp")
	identity.Username = env.name("frank")
	user, err := env.resolve(t, identity, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != env.name("frank")+"_2" {
		t.Fatalf("username = %q", user.Username)
	}
}

func TestExternalSignInRoleSyncDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	identity := env.identity("gina")
	identity.ManagedRole = pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN
	user, err := env.resolve(t, identity, true, 0)
	if err != nil || user.Role != models.RoleAdmin || user.AccessGroupID != nil {
		t.Fatalf("created admin = %+v, %v", user, err)
	}
	// Another enabled admin exists, so the demotion goes through and the
	// account's sessions are revoked.
	env.localAccount(t, "other-admin", models.RoleAdmin)
	identity.ManagedRole = pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_USER
	demoted, err := env.resolve(t, identity, true, 0)
	if err != nil || demoted.Role != models.RoleUser {
		t.Fatalf("demoted = %+v, %v", demoted, err)
	}
	if len(env.revoked) != 1 || env.revoked[0] != user.ID {
		t.Fatalf("revoked = %v", env.revoked)
	}
	// UNSPECIFIED leaves the role alone.
	identity.ManagedRole = pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_UNSPECIFIED
	if same, err := env.resolve(t, identity, true, 0); err != nil || same.Role != models.RoleUser {
		t.Fatalf("unspecified = %+v, %v", same, err)
	}
	identity.ManagedRole = pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN
	if promoted, err := env.resolve(t, identity, true, 0); err != nil || promoted.Role != models.RoleAdmin {
		t.Fatalf("promoted = %+v, %v", promoted, err)
	}
}

func TestExternalSignInNeverDemotesProtectedAdminsDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	for _, tc := range []struct {
		name   string
		setup  string
		reason string
	}{
		{"owner", `UPDATE users SET is_owner = true WHERE id = $1`, "owner"},
		{"break-glass", `UPDATE users SET break_glass = true WHERE id = $1`, "break_glass"},
		{"last enabled admin", `UPDATE users SET enabled = false WHERE role = 'admin' AND id <> $1`, "last_admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin := env.localAccount(t, "protected-"+strings.ReplaceAll(tc.name, " ", "-"), models.RoleAdmin)
			// Run inside a transaction that is rolled back, so the owner flag
			// and the disabled admins never reach other tests.
			tx, err := env.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if tc.name == "owner" {
				if _, err := tx.Exec(t.Context(), `UPDATE users SET is_owner = false WHERE is_owner`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tx.Exec(t.Context(), tc.setup, admin.ID); err != nil {
				t.Fatal(err)
			}
			current, err := userByID(t.Context(), tx, admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			updated, event, err := syncManagedRole(t.Context(), tx, current, pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_USER)
			if err != nil {
				t.Fatal(err)
			}
			if updated != nil {
				t.Fatalf("demoted a protected admin: %+v", updated)
			}
			if event == nil || event.event != "role_change_skipped" || !containsAttr(event.attrs, "reason", tc.reason) {
				t.Fatalf("audit = %+v", event)
			}
		})
	}

	// Break-glass protects local recovery: a break-glass account that cannot
	// use its password gets no immunity from provider demotion.
	env.localAccount(t, "protected-other-admin", models.RoleAdmin)
	glass := env.localAccount(t, "protected-glass-no-password", models.RoleAdmin)
	if _, err := env.pool.Exec(t.Context(), `UPDATE users SET break_glass = true, local_password_login_enabled = false WHERE id = $1`, glass.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := env.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	current, err := userByID(t.Context(), tx, glass.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated, _, err := syncManagedRole(t.Context(), tx, current, pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_USER); err != nil || updated == nil || updated.Role != models.RoleUser {
		t.Fatalf("break-glass without a password = %+v, %v", updated, err)
	}
}

func containsAttr(attrs []any, key string, value any) bool {
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == key && attrs[i+1] == value {
			return true
		}
	}
	return false
}

func TestLocalPasswordPolicyDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	users := NewUserRepository(env.pool)
	local := NewLocalProvider(users, NewSessionRepository(env.pool))
	member := env.localAccount(t, "member", models.RoleUser)
	admin := env.localAccount(t, "glass-admin", models.RoleAdmin)

	// Turning local sign-in off needs a usable break-glass admin, from the
	// settings path and the CLI path alike. Other tests' break-glass rows
	// would satisfy it, so count first.
	if env.usableBreakGlass(t) == 0 {
		if err := CheckLocalLoginSettingChange(ctx, env.pool, "true", "false"); !errors.Is(err, ErrBreakGlassRequired) {
			t.Fatalf("settings check = %v, want ErrBreakGlassRequired", err)
		}
	}
	glass := true
	if err := users.Update(ctx, admin.ID, models.UpdateUserInput{BreakGlass: &glass}); err != nil {
		t.Fatal(err)
	}
	if err := CheckLocalLoginSettingChange(ctx, env.pool, "true", "false"); err != nil {
		t.Fatalf("settings check with break-glass = %v", err)
	}
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")

	creds := Credentials{Username: member.Username, Password: "correct horse battery"}
	if _, err := local.Authenticate(ctx, creds); !errors.Is(err, ErrLocalLoginDisabled) {
		t.Fatalf("member sign-in = %v, want ErrLocalLoginDisabled", err)
	}
	// A wrong password is still just a wrong password.
	if _, err := local.Authenticate(ctx, Credentials{Username: member.Username, Password: "wrong password"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password = %v, want ErrInvalidCredentials", err)
	}
	if _, err := local.Authenticate(ctx, Credentials{Username: admin.Username, Password: "correct horse battery"}); err != nil {
		t.Fatalf("break-glass sign-in = %v", err)
	}

	// The last usable break-glass admin cannot lose the flag, its role or
	// its password sign-in while local sign-in is off.
	for name, input := range map[string]*models.UpdateUserInput{
		"clear flag":    {BreakGlass: new(false)},
		"demote":        {Role: new(models.RoleUser)},
		"disable":       {Enabled: new(false)},
		"password off":  {LocalPasswordLoginEnabled: new(false)},
		"delete (nil)":  nil,
		"harmless edit": {MaxProfiles: models.SetValue(3)},
	} {
		t.Run(name, func(t *testing.T) {
			if count := env.usableBreakGlass(t); count != 1 {
				t.Skipf("%d break-glass accounts in the database; the last-one rule needs exactly this one", count)
			}
			current, err := users.GetByID(ctx, admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			err = pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
				return EnsureBreakGlassAfterAdminChange(ctx, tx, current, input)
			})
			if name == "harmless edit" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrBreakGlassRequired) {
				t.Fatalf("err = %v, want ErrBreakGlassRequired", err)
			}
		})
	}

	// The recovery command turns the switch back on.
	if err := users.EnableLocalPasswordLogin(ctx); err != nil {
		t.Fatal(err)
	}
	if allowed, err := users.LocalPasswordLoginAllowed(ctx); err != nil || !allowed {
		t.Fatalf("after recovery: allowed = %v, %v", allowed, err)
	}

	// A role change away from admin clears the flag (the constraint allows
	// it only on admins).
	demote := models.RoleUser
	if err := users.Update(ctx, admin.ID, models.UpdateUserInput{Role: &demote}); err != nil {
		t.Fatal(err)
	}
	if after, _ := users.GetByID(ctx, admin.ID); after.BreakGlass {
		t.Fatal("demotion kept break_glass")
	}
	if _, err := local.Authenticate(ctx, creds); err != nil {
		t.Fatalf("member sign-in after re-enable = %v", err)
	}
}

// credentialsDirectory is a password provider standing in for an LDAP
// plugin; it records the names it was asked about.
type credentialsDirectory struct{ asked []string }

func (d *credentialsDirectory) Authenticate(_ context.Context, creds Credentials) (*models.User, error) {
	d.asked = append(d.asked, creds.Username)
	return nil, ErrInvalidCredentials
}
func (d *credentialsDirectory) ValidateSession(context.Context, string) (bool, error) {
	return true, nil
}

type staticProviderSource []RegisteredProvider

func (s staticProviderSource) Providers() []RegisteredProvider { return s }

func TestPasswordLoginRoutingDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	users := NewUserRepository(env.pool)
	svc := NewService(NewLocalProvider(users, NewSessionRepository(env.pool)), nil, NewSessionRepository(env.pool), users, nil, nil, nil)
	directory := &credentialsDirectory{}
	svc.SetPluginProviderSource(staticProviderSource{{
		Info:     LoginProviderInfo{ID: PluginProviderID(env.installationID, "ldap"), DisplayName: "Directory", Mode: "credentials", InstallationID: env.installationID},
		Provider: directory,
	}})
	member := env.localAccount(t, "route-member", models.RoleUser)
	admin := env.localAccount(t, "route-glass", models.RoleAdmin)
	glass := true
	if err := users.Update(ctx, admin.ID, models.UpdateUserInput{BreakGlass: &glass}); err != nil {
		t.Fatal(err)
	}
	linked := env.localAccount(t, "route-linked", models.RoleUser)
	off := false
	if err := users.Update(ctx, linked.ID, models.UpdateUserInput{LocalPasswordLoginEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	directoryID := PluginProviderID(env.installationID, "ldap")
	route := func(name string) string {
		t.Helper()
		id, err := svc.routePasswordLogin(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if got := route(member.Username); got != LocalProviderID {
		t.Fatalf("local account routed to %s", got)
	}
	if got := route(member.Email); got != LocalProviderID {
		t.Fatalf("local account by email routed to %s", got)
	}
	if got := route(linked.Username); got != directoryID {
		t.Fatalf("account without local password routed to %s", got)
	}
	if got := route(env.name("stranger")); got != directoryID {
		t.Fatalf("unknown name routed to %s", got)
	}
	// With local sign-in off an account with a local password still routes
	// locally (and is refused there): its password never reaches the
	// directory.
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	if got := route(member.Username); got != LocalProviderID {
		t.Fatalf("with local sign-in off, a local account routed to %s", got)
	}
	if got := route(admin.Username); got != LocalProviderID {
		t.Fatalf("break-glass admin routed to %s", got)
	}
	if _, _, err := svc.CompatLogin(ctx, member.Username, "correct horse battery", "test", ""); !errors.Is(err, ErrLocalLoginDisabled) {
		t.Fatalf("compat login of a local account with local sign-in off = %v", err)
	}
	for _, asked := range directory.asked {
		if asked == member.Username {
			t.Fatal("a local account's password reached the directory")
		}
	}
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")

	// Discovery leaves the local provider out while local sign-in is off; the
	// directory still takes passwords.
	discovery, err := svc.DiscoverProviders(ctx)
	if err != nil || len(discovery.Providers) != 2 || !discovery.PasswordLogin {
		t.Fatalf("discovery on = %+v, %v", discovery, err)
	}
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	discovery, err = svc.DiscoverProviders(ctx)
	if err != nil || len(discovery.Providers) != 1 || discovery.Providers[0].ID != directoryID || !discovery.Providers[0].Default || !discovery.PasswordLogin {
		t.Fatalf("discovery off = %+v, %v", discovery, err)
	}
}

func TestIdentityServiceDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	svc := NewIdentityService(env.pool)

	// An account created by the provider has no local password: its only
	// identity cannot be disconnected.
	created, err := env.resolve(t, env.identity("hana"), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := svc.ListForUser(ctx, created.ID)
	if err != nil || len(identities) != 1 {
		t.Fatalf("identities = %+v, %v", identities, err)
	}
	if err := svc.UnlinkOwn(ctx, created.ID, identities[0].ID); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("unlink last = %v, want ErrLastSignInMethod", err)
	}
	if can, err := svc.CanUnlinkOwn(ctx, created.ID); err != nil || can {
		t.Fatalf("can unlink the only way in = %v, %v", can, err)
	}
	// Someone else's identity id is not found.
	other := env.localAccount(t, "ivan", models.RoleUser)
	if err := svc.UnlinkOwn(ctx, other.ID, identities[0].ID); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("unlink foreign = %v, want ErrIdentityNotFound", err)
	}

	// An administrator links a local account; linking turns its password
	// off, so it cannot unlink itself until the password is back on.
	subject := "https://id.example.test|" + env.name("ivan-idp")
	linked, err := svc.AdminLink(ctx, AdminLinkInput{UserID: other.ID, InstallationID: env.installationID, Identity: ExternalIdentity{Subject: subject, Username: "ivan"}})
	if err != nil || linked.LastSignInAt != nil {
		t.Fatalf("admin link = %+v, %v", linked, err)
	}
	if _, err := svc.AdminLink(ctx, AdminLinkInput{UserID: created.ID, InstallationID: env.installationID, Identity: ExternalIdentity{Subject: subject}}); !errors.Is(err, ErrIdentityLinkedElsewhere) {
		t.Fatalf("link elsewhere = %v", err)
	}
	if _, err := svc.AdminLink(ctx, AdminLinkInput{UserID: other.ID, InstallationID: -1, Identity: ExternalIdentity{Subject: "x"}}); !errors.Is(err, ErrUnknownAuthInstallation) {
		t.Fatalf("unknown installation = %v", err)
	}
	if err := svc.UnlinkOwn(ctx, other.ID, linked.ID); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("unlink without password = %v", err)
	}
	on := true
	if err := NewUserRepository(env.pool).Update(ctx, other.ID, models.UpdateUserInput{LocalPasswordLoginEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	// The account's password counts only while the server lets it sign in.
	t.Cleanup(saveSettings(t, env.pool, config.AuthLocalPasswordLoginSettingKey))
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	if can, err := svc.CanUnlinkOwn(ctx, other.ID); err != nil || can {
		t.Fatalf("can unlink with passwords off = %v, %v", can, err)
	}
	if err := svc.UnlinkOwn(ctx, other.ID, linked.ID); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("unlink with passwords off = %v", err)
	}
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	if can, err := svc.CanUnlinkOwn(ctx, other.ID); err != nil || !can {
		t.Fatalf("can unlink with a password = %v, %v", can, err)
	}
	if err := svc.UnlinkOwn(ctx, other.ID, linked.ID); err != nil {
		t.Fatalf("unlink with password = %v", err)
	}
	// The administrator may unlink the provider-created account's identity.
	if err := svc.AdminUnlink(ctx, created.ID, identities[0].ID, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.AdminUnlink(ctx, created.ID, identities[0].ID, 0, false); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("second unlink = %v", err)
	}
}

func TestIdentityServiceRequiresEnabledFallbackDB(t *testing.T) {
	for _, disabled := range []string{"binding", "installation"} {
		t.Run(disabled, func(t *testing.T) {
			env := newExternalSignInEnv(t)
			ctx := t.Context()
			user, err := env.resolve(t, env.identity("old-provider"), true, 0)
			if err != nil {
				t.Fatal(err)
			}
			old := env.identityRow(t, env.identity("old-provider").Subject)
			if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID); err != nil {
				t.Fatal(err)
			}
			var currentInstallation int
			if err := env.pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
				VALUES ($1, '0', '/nonexistent/identity-fallback-test', true, 'manual', 'plugin') RETURNING id`,
				"identity-fallback-"+env.suffix).Scan(&currentInstallation); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM plugin_installations WHERE id = $1`, currentInstallation)
			})
			if _, err := env.pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'oidc', false)`, currentInstallation); err != nil {
				t.Fatal(err)
			}
			svc := NewIdentityService(env.pool)
			current, err := svc.AdminLink(ctx, AdminLinkInput{UserID: user.ID, InstallationID: currentInstallation,
				Identity: ExternalIdentity{Subject: "current-" + env.suffix}})
			if err != nil {
				t.Fatal(err)
			}
			if disabled == "binding" {
				_, err = env.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = false WHERE plugin_installation_id = $1`, env.installationID)
			} else {
				_, err = env.pool.Exec(ctx, `UPDATE plugin_installations SET enabled = false WHERE id = $1`, env.installationID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, currentInstallation); err != nil {
				t.Fatal(err)
			}
			if can, err := svc.CanUnlinkOwn(ctx, user.ID); err != nil || can {
				t.Fatalf("can unlink the only enabled identity = %v, %v", can, err)
			}
			if err := svc.UnlinkOwn(ctx, user.ID, current.ID); !errors.Is(err, ErrLastSignInMethod) {
				t.Fatalf("unlink the only enabled identity = %v", err)
			}
			// The inactive link may still be removed: the current provider is
			// a usable fallback, even while the collection flag is conservative.
			if err := svc.UnlinkOwn(ctx, user.ID, old.ID); err != nil {
				t.Fatalf("unlink the disabled provider = %v", err)
			}
			identities, err := svc.ListForUser(ctx, user.ID)
			if err != nil || len(identities) != 1 || identities[0].ID != current.ID {
				t.Fatalf("remaining identities = %+v, %v", identities, err)
			}
		})
	}
}

// TestConcurrentFirstSignInCreatesOneAccountDB: two nodes resolving the same
// new person at once end with one account.
func TestConcurrentFirstSignInCreatesOneAccountDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	identity := env.identity("jules")
	results := make(chan int, 4)
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			user, _, err := env.resolver.Resolve(context.Background(), ResolveInput{InstallationID: env.installationID, AutoProvision: true, Identity: identity})
			if err != nil {
				errs <- err
				return
			}
			results <- user.ID
		}()
	}
	ids := map[int]bool{}
	for range 4 {
		select {
		case id := <-results:
			ids[id] = true
		case err := <-errs:
			t.Fatal(err)
		case <-time.After(30 * time.Second):
			t.Fatal("timed out")
		}
	}
	if len(ids) != 1 {
		t.Fatalf("accounts = %v", ids)
	}
}

// An account linked to a provider has no local password sign-in, so a
// provider outage or plugin removal must not strand it: the recovery command
// turns local sign-in back on for one account, optionally with a temporary
// password that revokes its sign-ins.
func TestEnableAccountLocalLoginDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	users := NewUserRepository(env.pool)
	owner := env.localAccount(t, "stranded", models.RoleAdmin)
	if _, err := env.resolve(t, env.identity("stranded-idp"), false, owner.ID); err != nil {
		t.Fatal(err)
	}
	if linked, _ := users.GetByID(ctx, owner.ID); linked.LocalPasswordLoginEnabled {
		t.Fatal("linking kept local password sign-in")
	}
	sessions := NewSessionRepository(env.pool)
	if err := sessions.Create(ctx, models.AuthSession{ID: "11111111-1111-4111-8111-" + fmt.Sprintf("%012d", owner.ID), UserID: owner.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if _, err := users.EnableAccountLocalLogin(ctx, "nobody-"+env.suffix, ""); !IsNotFound(err) {
		t.Fatalf("unknown account: %v", err)
	}
	user, err := users.EnableAccountLocalLogin(ctx, owner.Username, "")
	if err != nil || !user.LocalPasswordLoginEnabled || user.PasswordChangeRequired {
		t.Fatalf("enable = %+v, %v", user, err)
	}
	if _, err := NewLocalProvider(users, sessions).Authenticate(ctx, Credentials{Username: owner.Username, Password: "correct horse battery"}); err != nil {
		t.Fatalf("sign-in with the kept password: %v", err)
	}

	user, err = users.EnableAccountLocalLogin(ctx, owner.Email, "temporary-pass-1")
	if err != nil || !user.PasswordChangeRequired || !CheckPassword(user, "temporary-pass-1") {
		t.Fatalf("temporary password = %+v, %v", user, err)
	}
	var live int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1 AND revoked_at IS NULL`, owner.ID).Scan(&live); err != nil || live != 0 {
		t.Fatalf("live sessions after a temporary password = %d, %v", live, err)
	}
}

// A signup creates a local-password account, so while local password
// sign-in is off it is refused before the invite code is spent, and
// discovery reports signup off.
func TestSignupRefusedWhileLocalLoginIsOffDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, "signup.enabled"))
	env.setSetting(t, "signup.enabled", "true")
	creator := env.localAccount(t, "signup-creator", models.RoleAdmin)
	code := env.name("signup-code")
	if _, err := env.pool.Exec(ctx, `INSERT INTO invite_codes (code, max_uses, created_by) VALUES ($1, 5, $2)`, code, creator.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = env.pool.Exec(context.Background(), `DELETE FROM invite_codes WHERE code = $1`, code) })
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("signup-local-login-test-secret-0123456789", time.Minute, time.Hour),
		sessions, users, NewInviteCodeRepository(env.pool), dbSettings{env}, pgstore.NewPostgresProvider(env.pool))

	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	if enabled, err := svc.IsSignupEnabled(ctx); err != nil || enabled {
		t.Fatalf("signup enabled with local sign-in off = %v, %v", enabled, err)
	}
	name := env.name("signup-new")
	if _, _, err := svc.Signup(ctx, name, name+"@example.test", "correct horse battery", code, false, "", "test", ""); !errors.Is(err, ErrLocalLoginDisabled) {
		t.Fatalf("signup = %v, want ErrLocalLoginDisabled", err)
	}
	var uses int
	if err := env.pool.QueryRow(ctx, `SELECT use_count FROM invite_codes WHERE code = $1`, code).Scan(&uses); err != nil || uses != 0 {
		t.Fatalf("invite code uses = %d, %v", uses, err)
	}
	if _, err := users.GetByUsername(ctx, name); !IsNotFound(err) {
		t.Fatalf("refused signup created an account: %v", err)
	}

	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	if enabled, err := svc.IsSignupEnabled(ctx); err != nil || !enabled {
		t.Fatalf("signup enabled with local sign-in on = %v, %v", enabled, err)
	}
	if _, _, err := svc.Signup(ctx, name, name+"@example.test", "correct horse battery", code, false, "", "test", ""); err != nil {
		t.Fatalf("signup with local sign-in on = %v", err)
	}
}

// TestExternalSignInEmailAutoMatchRevokesCredentialsDB: Silo never verified
// the email an invited account registered with, so whoever registered it may
// hold the account's credentials. Email auto-match links the account but ends
// every credential the provider did not vouch for: login and Audiobookshelf
// sessions, device approvals and API keys. The sessions are revoked, not
// moved under the identity.
func TestExternalSignInEmailAutoMatchRevokesCredentialsDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	env.setSetting(t, config.AuthEmailAutoMatchSettingKey, "true")

	creator := env.localAccount(t, "match-inviter", models.RoleAdmin)
	code := env.name("match-invite")
	if _, err := env.pool.Exec(ctx, `INSERT INTO invite_codes (code, max_uses, created_by) VALUES ($1, 1, $2)`, code, creator.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM invite_codes WHERE code = $1`, code)
	})
	users := NewUserRepository(env.pool)
	invited, err := NewAccountProvisioner(users, pgstore.NewPostgresProvider(env.pool)).CreateInvitedAccount(ctx, CreateAccountInput{
		User: models.CreateUserInput{Username: env.name("match-invited"), Email: env.name("victim") + "@example.test",
			Password: "correct horse battery", Role: models.RoleUser},
	}, code)
	if err != nil {
		t.Fatalf("invited signup: %v", err)
	}

	sessions := NewSessionRepository(env.pool)
	jwt := NewJWTService("email-match-test-jwt-secret", time.Hour, 24*time.Hour)
	svc := NewService(NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)
	session := models.AuthSession{ID: uuid.New().String(), UserID: invited.ID, DeviceName: "email-match", ExpiresAt: time.Now().Add(24 * time.Hour)}
	if err := sessions.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	pair, err := svc.generateTokenPair(Claims{UserID: invited.ID, Role: invited.Role, SessionID: session.ID})
	if err != nil {
		t.Fatal(err)
	}
	keys := NewAPIKeyRepository(env.pool)
	key, err := keys.Create(ctx, invited.ID, "email-match", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'abs-device')`,
		invited.ID, "abs-"+env.suffix); err != nil {
		t.Fatal(err)
	}

	verified := true
	identity := env.identity("victim-idp")
	identity.Email = invited.Email
	identity.EmailVerified = &verified
	user, err := env.resolve(t, identity, true, 0)
	if err != nil || user.ID != invited.ID {
		t.Fatalf("auto-match = %+v, %v; want account %d", user, err, invited.ID)
	}

	if _, err := svc.Refresh(ctx, pair.RefreshToken); err == nil {
		t.Fatal("the matched account's earlier login session still refreshes")
	}
	row, err := sessions.GetByID(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.RevokedAt == nil || row.IdentityID != nil {
		t.Fatalf("earlier session revoked_at = %v, identity_id = %v; want revoked and not attached", row.RevokedAt, row.IdentityID)
	}
	if _, err := keys.GetByKey(ctx, key.Key); err == nil {
		t.Fatal("the matched account kept its API key")
	}
	var liveABS int
	if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL`, invited.ID).Scan(&liveABS); err != nil {
		t.Fatal(err)
	}
	if liveABS != 0 {
		t.Fatalf("live Audiobookshelf sessions = %d, want 0", liveABS)
	}
	if len(env.revoked) != 1 || env.revoked[0] != invited.ID {
		t.Fatalf("session hook ran for %v, want [%d]", env.revoked, invited.ID)
	}
}

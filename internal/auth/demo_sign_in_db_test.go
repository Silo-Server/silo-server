package auth

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

// countedSettings counts the setting reads that reach its store, and fails
// them all while fail is set.
type countedSettings struct {
	store demomode.Reader
	reads atomic.Int32
	fail  atomic.Bool
}

func (c *countedSettings) Get(ctx context.Context, key string) (string, error) {
	c.reads.Add(1)
	if c.fail.Load() {
		return "", errors.New("settings store unreachable")
	}
	return c.store.Get(ctx, key)
}

const demoRight, demoWrong = "correct horse battery", "wrong horse battery"

// demoSignInListener signs in the way one sign-in surface does, through a
// Service of its own sharing the fixture's limiter, with d.
type demoSignInListener struct {
	name   string
	signIn func(d *DemoSignIn, name, password string) error
}

// demoSignInFixture is a server with the three password sign-in surfaces,
// one limiter allowing one attempt per name, and a DemoSignIn reading the
// test database's settings through settings.
type demoSignInFixture struct {
	env       *externalSignInEnv
	limiter   *countingSignInAttempts
	settings  *countedSettings
	demo      *DemoSignIn
	users     *UserRepository
	listeners []demoSignInListener
	newSvc    func(*DemoSignIn) *Service
}

func newDemoSignInFixture(t *testing.T) *demoSignInFixture {
	t.Helper()
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, demomode.SettingKey, demomode.AccountSettingKey, config.AuthLocalPasswordLoginSettingKey))
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	users, sessions := NewUserRepository(env.pool), NewSessionRepository(env.pool)
	f := &demoSignInFixture{env: env, limiter: newCountingSignInAttempts(1), settings: &countedSettings{store: poolSettings{env}}, users: users}
	f.demo = NewDemoSignIn(users, demomode.New(f.settings))
	f.newSvc = func(d *DemoSignIn) *Service {
		svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-demo-signin-secret-0123456789", time.Minute, 24*time.Hour), sessions, users, nil, nil, nil)
		svc.SetSignInAttempts(f.limiter)
		svc.SetDemoSignIn(d)
		return svc
	}
	f.listeners = []demoSignInListener{
		{"native", func(d *DemoSignIn, name, password string) error {
			_, _, err := f.newSvc(d).LoginWithProvider(ctx, "", name, password, "test", "192.0.2.10")
			return err
		}},
		{"jellyfin", func(d *DemoSignIn, name, password string) error {
			basePw, _ := SplitPasswordPIN(password)
			_, _, _, err := f.newSvc(d).CompatLoginWithLocalFallback(ctx, name, password, basePw, "test", "192.0.2.10", name)
			return err
		}},
		{"audiobookshelf", func(d *DemoSignIn, name, password string) error {
			basePw, _ := SplitPasswordPIN(password)
			_, _, _, err := f.newSvc(d).CompatLoginWithLocalFallback(ctx, name, password, basePw, "abs-compat", "", "")
			return err
		}},
	}
	return f
}

// reset forgets the counts and the addresses earlier sign-ins made known,
// so each case starts on an unused budget.
func (f *demoSignInFixture) reset() { f.limiter.expire(); clear(f.limiter.known) }

// name sets the demo settings and refreshes the exempt names.
func (f *demoSignInFixture) name(t *testing.T, enabled, account string) {
	t.Helper()
	f.env.setSetting(t, demomode.SettingKey, enabled)
	f.env.setSetting(t, demomode.AccountSettingKey, account)
	f.demo.Refresh(t.Context())
}

// throttledAfterFailure reports whether the correct password is refused by
// the per-name budget after one failed attempt.
func throttledAfterFailure(t *testing.T, signIn func(name, password string) error, name string) (bool, error) {
	t.Helper()
	if _, throttled := errors.AsType[*SignInThrottledError](signIn(name, demoWrong)); throttled {
		t.Fatal("the first attempt was refused")
	}
	err := signIn(name, demoRight)
	_, throttled := errors.AsType[*SignInThrottledError](err)
	return throttled, err
}

func demoID(u *models.User) string { return strconv.Itoa(u.ID) }

// TestDemoSignInNameBudgetDB: with demo mode on and demo.account_id naming
// the shared account, the per-name budget does not refuse that account's
// correct password, on the native, Jellyfin-compatible and
// Audiobookshelf-compatible sign-ins (each its own Service sharing one
// limiter and one DemoSignIn, as the server wires them). Every other case
// keeps the ordinary budget. Catches the exemption reaching a name, account
// or state it must not, and an attempt for another name reading anything
// the exemption needs.
func TestDemoSignInNameBudgetDB(t *testing.T) {
	f := newDemoSignInFixture(t)
	env := f.env
	visitor := env.localAccount(t, "demo-visitor", models.RoleUser)
	other := env.localAccount(t, "demo-other", models.RoleUser)
	admin := env.localAccount(t, "demo-admin", models.RoleAdmin)
	disabled := env.localAccount(t, "demo-disabled", models.RoleUser)
	deleted := env.localAccount(t, "demo-deleted", models.RoleUser)
	if _, err := env.pool.Exec(t.Context(), `UPDATE users SET enabled = false WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1`, deleted.ID); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, enabled, account, signInAs string
		exempt                           bool
	}{
		{"demo account", demomode.OnValue, demoID(visitor), visitor.Username, true},
		{"demo account by email", demomode.OnValue, demoID(visitor), visitor.Email, true},
		{"demo mode off", "false", demoID(visitor), visitor.Username, false},
		{"setting empty", demomode.OnValue, "", visitor.Username, false},
		{"administrator", demomode.OnValue, demoID(admin), admin.Username, false},
		{"disabled account", demomode.OnValue, demoID(disabled), disabled.Username, false},
		{"deleted account", demomode.OnValue, demoID(deleted), deleted.Username, false},
		{"another name", demomode.OnValue, demoID(visitor), other.Username, false},
	} {
		for _, l := range f.listeners {
			t.Run(tc.name+"/"+l.name, func(t *testing.T) {
				defer f.reset()
				f.name(t, tc.enabled, tc.account)
				before := f.settings.reads.Load()
				signIn := func(name, password string) error { return l.signIn(f.demo, name, password) }
				throttled, err := throttledAfterFailure(t, signIn, tc.signInAs)
				switch {
				case tc.exempt && err != nil:
					t.Fatalf("correct password after a failure: %v, want a sign-in", err)
				case !tc.exempt && !throttled:
					t.Fatalf("correct password after a failure: %v, want the per-name budget's refusal", err)
				}
				// The exemption reads nothing for a name that is not the demo
				// account's: its settings and account reads happen only after
				// a name's key matched.
				if reads := f.settings.reads.Load() - before; tc.name == "another name" && reads != 0 {
					t.Fatalf("another name's attempts read %d settings, want none", reads)
				}
			})
		}
	}
}

// TestDemoSignInFailsClosedDB: the per-name budget still applies when the
// settings were never read, when the named account became an administrator
// after the names were last refreshed, and when the password goes to a
// provider other than the local one. Catches the exemption trusting a stale
// or unreadable answer, or covering a password the demo does not share.
func TestDemoSignInFailsClosedDB(t *testing.T) {
	f := newDemoSignInFixture(t)
	env, ctx := f.env, t.Context()
	native := func(d *DemoSignIn) func(name, password string) error {
		return func(name, password string) error { return f.listeners[0].signIn(d, name, password) }
	}

	t.Run("the setting unreadable with no earlier value", func(t *testing.T) {
		visitor := env.localAccount(t, "demo-cold", models.RoleUser)
		f.name(t, demomode.OnValue, demoID(visitor))
		// A process that has never read the settings, holding the names
		// another refresh found: the exemption checks again and fails closed.
		unread := &countedSettings{store: poolSettings{env}}
		unread.fail.Store(true)
		cold := NewDemoSignIn(f.users, demomode.New(unread))
		cold.keys.Store(f.demo.keys.Load())
		for _, l := range f.listeners {
			f.reset()
			signIn := func(name, password string) error { return l.signIn(cold, name, password) }
			if throttled, err := throttledAfterFailure(t, signIn, visitor.Username); !throttled {
				t.Fatalf("%s: %v, want the per-name budget's refusal", l.name, err)
			}
		}
	})

	t.Run("an account promoted after the setting was written", func(t *testing.T) {
		defer f.reset()
		promoted := env.localAccount(t, "demo-promoted", models.RoleUser)
		f.name(t, demomode.OnValue, demoID(promoted))
		if _, err := env.pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, promoted.ID); err != nil {
			t.Fatal(err)
		}
		if throttled, err := throttledAfterFailure(t, native(f.demo), promoted.Username); !throttled {
			t.Fatalf("%v, want the per-name budget's refusal", err)
		}
	})

	t.Run("a named provider other than the local one", func(t *testing.T) {
		defer f.reset()
		visitor := env.localAccount(t, "demo-directory", models.RoleUser)
		f.name(t, demomode.OnValue, demoID(visitor))
		svc := f.newSvc(f.demo)
		ldap := PluginProviderID(env.installationID, "ldap")
		svc.SetPluginProviderSource(staticProviderSource{{
			Info:     LoginProviderInfo{ID: ldap, Mode: ProviderModeCredentials, InstallationID: env.installationID},
			Provider: &credentialsDirectory{},
		}})
		signIn := func(name, password string) error {
			_, _, err := svc.LoginWithProvider(ctx, ldap, name, password, "test", "192.0.2.10")
			return err
		}
		if throttled, err := throttledAfterFailure(t, signIn, visitor.Username); !throttled {
			t.Fatalf("a directory sign-in with the demo account's name: %v, want the per-name budget's refusal", err)
		}
	})
}

// TestCheckDemoAccountSettingDB: writing demo.account_id refuses an account
// that does not exist or is an administrator's, and accepts empty and an
// ordinary account. Catches the setting naming an administrator, whose
// per-name budget would then never apply.
func TestCheckDemoAccountSettingDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	visitor := env.localAccount(t, "demo-check-visitor", models.RoleUser)
	admin := env.localAccount(t, "demo-check-admin", models.RoleAdmin)
	for value, want := range map[string]error{
		"":                       nil,
		strconv.Itoa(visitor.ID): nil,
		strconv.Itoa(admin.ID):   ErrDemoAccountAdmin,
		"2147483647":             ErrDemoAccountNotFound,
		"not-an-id":              ErrDemoAccountNotFound,
	} {
		if err := CheckDemoAccountSetting(t.Context(), env.pool, value); !errors.Is(err, want) {
			t.Errorf("CheckDemoAccountSetting(%q) = %v, want %v", value, err, want)
		}
	}
}

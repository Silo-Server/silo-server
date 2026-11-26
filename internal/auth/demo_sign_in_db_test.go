package auth

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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

// statementLog records every statement a pool runs (a pgx query tracer).
type statementLog struct {
	mu  sync.Mutex
	sql []string
}

func (l *statementLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sql = append(l.sql, data.SQL)
	return ctx
}

func (*statementLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// during returns the statements run while do ran.
func (l *statementLog) during(do func()) []string {
	l.mu.Lock()
	start := len(l.sql)
	l.mu.Unlock()
	do()
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.sql[start:])
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
	// statements logs every statement run on env.pool.
	statements *statementLog
}

func newDemoSignInFixture(t *testing.T) *demoSignInFixture {
	t.Helper()
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	// Every statement the fixture's sign-ins run goes through one traced
	// pool, so a test can compare what two sign-ins ran.
	statements := &statementLog{}
	poolConfig, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Tracer = statements
	traced, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	env.pool = traced
	t.Cleanup(saveSettings(t, env.pool, demomode.SettingKey, demomode.AccountSettingKey, config.AuthLocalPasswordLoginSettingKey))
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	users, sessions := NewUserRepository(env.pool), NewSessionRepository(env.pool)
	f := &demoSignInFixture{env: env, limiter: newCountingSignInAttempts(1), settings: &countedSettings{store: poolSettings{env}}, users: users, statements: statements}
	f.demo = NewDemoSignIn(users, f.settings)
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
// or state it must not, and an attempt for another name running any
// statement it would not run without the exemption.
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
				signIn := func(name, password string) error { return l.signIn(f.demo, name, password) }
				var throttled bool
				var err error
				ran := f.statements.during(func() { throttled, err = throttledAfterFailure(t, signIn, tc.signInAs) })
				switch {
				case tc.exempt && err != nil:
					t.Fatalf("correct password after a failure: %v, want a sign-in", err)
				case !tc.exempt && !throttled:
					t.Fatalf("correct password after a failure: %v, want the per-name budget's refusal", err)
				}
				// A name that is not the demo account's runs exactly the
				// statements it runs with no exemption configured.
				if tc.name == "another name" {
					f.reset()
					unconfigured := f.statements.during(func() {
						_, _ = throttledAfterFailure(t, func(name, password string) error { return l.signIn(nil, name, password) }, tc.signInAs)
					})
					if !slices.Equal(ran, unconfigured) {
						t.Fatalf("another name ran %q with the demo account named, want %q as with none", ran, unconfigured)
					}
				}
			})
		}
	}
}

// TestDemoSignInFailsClosedDB: the per-name budget still applies when the
// settings or the account cannot be read at the attempt, whatever an
// earlier refresh found, when the named account became an administrator
// after the names were last refreshed. Catches the exemption trusting a
// stale or unreadable answer.
func TestDemoSignInFailsClosedDB(t *testing.T) {
	f := newDemoSignInFixture(t)
	env, ctx := f.env, t.Context()
	native := func(d *DemoSignIn) func(name, password string) error {
		return func(name, password string) error { return f.listeners[0].signIn(d, name, password) }
	}

	// Each case holds the names an earlier refresh found while demo mode was
	// on and the account named, then fails the read an attempt makes before
	// it is exempted: a failed read never falls back to the names or values
	// read before.
	for _, tc := range []struct {
		name string
		// cold returns the DemoSignIn and the settings reader it reads.
		cold func(t *testing.T) (*DemoSignIn, *countedSettings)
	}{
		{"every settings read fails", func(t *testing.T) (*DemoSignIn, *countedSettings) {
			// It read demo mode on and the account named, then the store
			// failed.
			failing := &countedSettings{store: poolSettings{env}}
			cold := NewDemoSignIn(f.users, failing)
			cold.Refresh(t.Context())
			failing.fail.Store(true)
			return cold, failing
		}},
		{"the settings unreadable with no earlier value", func(*testing.T) (*DemoSignIn, *countedSettings) {
			// A process that has never read the settings, holding the names
			// another refresh found.
			unread := &countedSettings{store: poolSettings{env}}
			unread.fail.Store(true)
			return NewDemoSignIn(f.users, unread), unread
		}},
		{"only the account read fails", func(t *testing.T) (*DemoSignIn, *countedSettings) {
			closed, err := pgxpool.New(ctx, os.Getenv("SILO_TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			closed.Close()
			return NewDemoSignIn(NewUserRepository(closed), f.settings), f.settings
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			visitor := env.localAccount(t, "demo-"+strings.ReplaceAll(tc.name, " ", "-"), models.RoleUser)
			f.name(t, demomode.OnValue, demoID(visitor))
			cold, reader := tc.cold(t)
			candidates := cold.keys.Load()
			if candidates == nil || len(*candidates) == 0 {
				candidates = f.demo.keys.Load()
			}
			for _, l := range f.listeners {
				f.reset()
				// A failed read clears the names, so they are put back before
				// every attempt: each one reaches the failed read.
				signIn := func(name, password string) error {
					cold.keys.Store(candidates)
					return l.signIn(cold, name, password)
				}
				before := reader.reads.Load()
				if throttled, err := throttledAfterFailure(t, signIn, visitor.Username); !throttled {
					t.Fatalf("%s: %v, want the per-name budget's refusal", l.name, err)
				}
				if reader.reads.Load() == before {
					t.Fatalf("%s: no attempt reached the demo settings read", l.name)
				}
			}
			// The same names with working reads are exempt, so the refusal
			// above came from the failed read.
			f.reset()
			if _, err := throttledAfterFailure(t, native(f.demo), visitor.Username); err != nil {
				t.Fatalf("with working reads: %v, want a sign-in", err)
			}
		})
	}

	t.Run("demo mode turned off, then every settings read fails", func(t *testing.T) {
		visitor := env.localAccount(t, "demo-turned-off", models.RoleUser)
		f.name(t, demomode.OnValue, demoID(visitor))
		failing := &countedSettings{store: poolSettings{env}}
		cold := NewDemoSignIn(f.users, failing)
		cold.Refresh(ctx)
		if known := cold.keys.Load(); known == nil || len(*known) == 0 {
			t.Fatal("the refresh found no names for the demo account")
		}
		env.setSetting(t, demomode.SettingKey, "false")
		env.setSetting(t, demomode.AccountSettingKey, "")
		failing.fail.Store(true)
		cold.Refresh(ctx)
		if known := cold.keys.Load(); known != nil && len(*known) != 0 {
			t.Fatalf("a refresh whose reads failed kept %d names", len(*known))
		}
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
}

// TestDemoSignInCanceledAttemptDB: an attempt whose request ends while it
// reads the demo settings is not exempted and leaves the names every other
// attempt relies on in place, while a read that fails for another reason
// clears them. Catches a visitor who drops the connection mid-sign-in
// switching the exemption off for everyone.
func TestDemoSignInCanceledAttemptDB(t *testing.T) {
	f := newDemoSignInFixture(t)
	ctx := t.Context()
	visitor := f.env.localAccount(t, "demo-canceled", models.RoleUser)
	f.name(t, demomode.OnValue, demoID(visitor))
	spend := f.newSvc(f.demo).newSignInSpend(ctx, visitor.Username, "")
	if !spend.folded {
		t.Fatal("the name was not folded by the database")
	}
	held := func(d *DemoSignIn) bool {
		known := d.keys.Load()
		return known != nil && slices.Contains(*known, spend.key)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if f.demo.exempts(canceled, spend, "") {
		t.Fatal("an attempt whose request ended was exempted")
	}
	if !held(f.demo) {
		t.Fatal("an attempt whose request ended cleared the demo account's names")
	}
	for _, l := range f.listeners {
		f.reset()
		signIn := func(name, password string) error { return l.signIn(f.demo, name, password) }
		if _, err := throttledAfterFailure(t, signIn, visitor.Username); err != nil {
			t.Fatalf("%s after a canceled attempt: %v, want a sign-in", l.name, err)
		}
	}

	failing := &countedSettings{store: poolSettings{f.env}}
	cold := NewDemoSignIn(f.users, failing)
	cold.Refresh(ctx)
	failing.fail.Store(true)
	if cold.exempts(ctx, spend, "") || held(cold) {
		t.Fatal("a read that failed with the request still live kept the names")
	}
}

// TestDemoSignInGuardsDB: each guard that keeps the exemption to the demo
// account's own local password, with a credentials directory enabled, where
// a name that does not sign in locally goes to the directory. Catches the
// exemption covering a sign-in that names another provider, an account
// without local password sign-in, a Jellyfin
// name whose budget name is the demo account's but which signs in to
// another account, and a stored email that sign-in does not look up.
func TestDemoSignInGuardsDB(t *testing.T) {
	f := newDemoSignInFixture(t)
	env, ctx := f.env, t.Context()
	withDirectory := func() *Service {
		svc := f.newSvc(f.demo)
		svc.SetPluginProviderSource(staticProviderSource{{
			Info:     LoginProviderInfo{ID: PluginProviderID(env.installationID, "ldap"), Mode: ProviderModeCredentials, InstallationID: env.installationID},
			Provider: &credentialsDirectory{},
		}})
		return svc
	}
	native := func(name, password string) error {
		_, _, err := withDirectory().LoginWithProvider(ctx, "", name, password, "test", "192.0.2.10")
		return err
	}
	jellyfin := func(budgetName string) func(name, password string) error {
		return func(name, password string) error {
			basePw, _ := SplitPasswordPIN(password)
			_, _, _, err := withDirectory().CompatLoginWithLocalFallback(ctx, name, password, basePw, "test", "192.0.2.10", budgetName)
			return err
		}
	}
	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	wantThrottled := func(t *testing.T, label string, signIn func(name, password string) error, name string) {
		t.Helper()
		defer f.reset()
		if throttled, err := throttledAfterFailure(t, signIn, name); !throttled {
			t.Fatalf("%s: %v, want the per-name budget's refusal", label, err)
		}
	}

	t.Run("a named provider other than the local one", func(t *testing.T) {
		visitor := env.localAccount(t, "demo-directory", models.RoleUser)
		f.name(t, demomode.OnValue, demoID(visitor))
		ldap := func(name, password string) error {
			_, _, err := withDirectory().LoginWithProvider(ctx, PluginProviderID(env.installationID, "ldap"), name, password, "test", "192.0.2.10")
			return err
		}
		wantThrottled(t, "directory", ldap, visitor.Username)
	})

	t.Run("an account without local password sign-in", func(t *testing.T) {
		directory := env.localAccount(t, "demo-no-local", models.RoleUser)
		exec(t, `UPDATE users SET local_password_login_enabled = false WHERE id = $1`, directory.ID)
		f.name(t, demomode.OnValue, demoID(directory))
		wantThrottled(t, "native", native, directory.Username)
		wantThrottled(t, "jellyfin", jellyfin(directory.Username), directory.Username)
	})

	t.Run("a Jellyfin budget name derived from another account's name", func(t *testing.T) {
		visitor := env.localAccount(t, "demo-derived", models.RoleUser)
		// An account whose own name is the demo account's plus "#Kids":
		// Jellyfin spends the budget of "<demo>" and signs in to it.
		kids := env.localAccount(t, "demo-derived-kids", models.RoleUser)
		exec(t, `UPDATE users SET username = $1 WHERE id = $2`, visitor.Username+"#Kids", kids.ID)
		f.name(t, demomode.OnValue, demoID(visitor))
		wantThrottled(t, "jellyfin", jellyfin(visitor.Username), visitor.Username+"#Kids")
	})

	t.Run("a stored email that sign-in does not look up", func(t *testing.T) {
		odd := env.localAccount(t, "demo-odd-email", models.RoleUser)
		// An older row can hold an address the sign-in lookup rejects, so
		// that name goes to the directory.
		oddEmail := env.name("demo-odd-email-no-at")
		exec(t, `UPDATE users SET email = $1 WHERE id = $2`, oddEmail, odd.ID)
		f.name(t, demomode.OnValue, demoID(odd))
		wantThrottled(t, "native", native, oddEmail)
		// Its username still signs in locally and is exempt.
		defer f.reset()
		if _, err := throttledAfterFailure(t, native, odd.Username); err != nil {
			t.Fatalf("the demo account's username: %v, want a sign-in", err)
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

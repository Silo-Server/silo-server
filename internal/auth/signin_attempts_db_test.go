package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// countingSignInAttempts allows max attempts per name, then refuses until
// the name is reset or its window passes (expire), except from an address
// trusted for the name, which gets max attempts of its own: the
// ratelimit.SignInLimiter without a clock or address grouping. This package
// cannot import ratelimit.
type countingSignInAttempts struct {
	max    int
	counts map[string]int
	known  map[string]bool
}

func newCountingSignInAttempts(maxAttempts int) *countingSignInAttempts {
	return &countingSignInAttempts{max: maxAttempts, counts: map[string]int{}, known: map[string]bool{}}
}

func (c *countingSignInAttempts) Reserve(_ context.Context, key, source string) (SignInLane, time.Duration, bool) {
	if c.counts[key] < c.max {
		c.counts[key]++
		return SignInLaneName, 0, true
	}
	pair := key + "|" + source
	if source == "" || !c.known[pair] {
		return SignInLaneName, 7 * time.Minute, false
	}
	if c.counts[pair] >= c.max {
		return SignInLaneSource, 7 * time.Minute, false
	}
	c.counts[pair]++
	return SignInLaneSource, 0, true
}

func (c *countingSignInAttempts) Reset(_ context.Context, key, source string, lane SignInLane) {
	if lane == SignInLaneSource {
		key += "|" + source
	}
	delete(c.counts, key)
}

func (c *countingSignInAttempts) Trust(_ context.Context, keys []string, source string) {
	for _, key := range keys {
		c.known[key+"|"+source] = true
	}
}

func (c *countingSignInAttempts) expire() { clear(c.counts) }

// Every password sign-in spends one budget per name, shared by the native
// and compatibility entry points. Unknown names are counted and refused like
// known ones, a locked name is refused before the password is checked, a
// correct password clears the count, and the account signs in again once
// the window has passed.
func TestSignInNameBudgetDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	user := env.localAccount(t, "signin-budget", models.RoleUser)
	users, sessions := NewUserRepository(env.pool), NewSessionRepository(env.pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-signin-budget-secret-0123456789", time.Minute, 24*time.Hour), sessions, users, nil, nil, nil)
	limiter := newCountingSignInAttempts(3)
	svc.SetSignInAttempts(limiter)

	const right, wrong = "correct horse battery", "wrong horse battery"
	native := func(name, password string) error {
		_, _, err := svc.LoginWithProvider(t.Context(), "", name, password, "test", "192.0.2.10")
		return err
	}
	compat := func(name, password string) error {
		basePw, _ := SplitPasswordPIN(password)
		_, _, _, err := svc.CompatLoginWithLocalFallback(t.Context(), name, password, basePw, "test", "192.0.2.10", "")
		return err
	}
	wantInvalid := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v, want ErrInvalidCredentials", err)
		}
	}
	wantThrottled := func(t *testing.T, err error) {
		t.Helper()
		throttled, ok := errors.AsType[*SignInThrottledError](err)
		if !ok || throttled.RetryAfter <= 0 {
			t.Fatalf("err = %v, want a *SignInThrottledError with the time left", err)
		}
	}

	for label, name := range map[string]string{"known name": user.Username, "unknown name": env.name("no-such-account")} {
		t.Run(label, func(t *testing.T) {
			defer limiter.expire()
			for range limiter.max {
				wantInvalid(t, native(name, wrong))
			}
			wantThrottled(t, native(name, right))
			wantThrottled(t, compat(name, right))
		})
	}

	t.Run("names fold case and space across surfaces", func(t *testing.T) {
		defer limiter.expire()
		wantInvalid(t, native("  "+user.Username+" ", wrong))
		wantInvalid(t, compat(user.Username, wrong))
		wantInvalid(t, native(user.Username, wrong))
		wantThrottled(t, compat(" "+strings.ToUpper(user.Username), right))
	})

	t.Run("a password#PIN spends two attempts for every name", func(t *testing.T) {
		defer limiter.expire()
		// With a directory, a name without a local account goes there and
		// never tries the fallback. It pays for one all the same, so the lock
		// does not tell local accounts apart.
		svc.SetPluginProviderSource(staticProviderSource{{
			Info:     LoginProviderInfo{ID: PluginProviderID(env.installationID, "ldap"), Mode: ProviderModeCredentials, InstallationID: env.installationID},
			Provider: &credentialsDirectory{},
		}})
		defer svc.SetPluginProviderSource(nil)
		for _, name := range []string{user.Username, env.name("no-such-account")} {
			wantInvalid(t, compat(name, "guess#1234"))
			wantInvalid(t, native(name, wrong))
			wantThrottled(t, native(name, right))
		}
	})

	t.Run("a temporary password clears the count on a compatibility sign-in", func(t *testing.T) {
		defer limiter.expire()
		temporary := env.localAccount(t, "signin-budget-temporary", models.RoleUser)
		if _, err := env.pool.Exec(t.Context(), `UPDATE users SET password_change_required = true WHERE id = $1`, temporary.ID); err != nil {
			t.Fatal(err)
		}
		for range limiter.max - 1 {
			wantInvalid(t, compat(temporary.Username, wrong))
		}
		if err := compat(temporary.Username, right); !errors.Is(err, ErrPasswordChangeRequired) {
			t.Fatalf("right temporary password: err = %v, want ErrPasswordChangeRequired", err)
		}
		if _, _, err := svc.LoginWithProvider(t.Context(), "", temporary.Username, right, "test", "192.0.2.10"); err != nil {
			t.Fatalf("native sign-in to change the password: %v", err)
		}
	})

	t.Run("a correct password clears the count", func(t *testing.T) {
		defer limiter.expire()
		for range limiter.max - 1 {
			wantInvalid(t, native(user.Username, wrong))
		}
		if err := native(user.Username, right); err != nil {
			t.Fatalf("right password: %v", err)
		}
		for range limiter.max {
			wantInvalid(t, compat(user.Username, wrong))
		}
		wantThrottled(t, native(user.Username, right))
		limiter.expire()
		if err := native(user.Username, right); err != nil {
			t.Fatalf("after the window: %v", err)
		}
	})

	// A sign-in spends the budget of the name typed, also when it signs in to
	// another account than the one that name finds: a Jellyfin name with a
	// '#' to an account whose own name has it, or a directory identity of a
	// separate account. That success must not clear the name's count or make
	// its address known for the name, or its owner could guess at the
	// account the name finds without end.
	t.Run("a sign-in to another account cannot clear a name's budget", func(t *testing.T) {
		victim := env.localAccount(t, "signin-budget-victim", models.RoleUser)
		hashed := env.localAccount(t, "signin-budget-hashed", models.RoleUser)
		if _, err := env.pool.Exec(t.Context(), `UPDATE users SET username = $1 WHERE id = $2`, victim.Username+"#x", hashed.ID); err != nil {
			t.Fatal(err)
		}
		// The directory's person has the victim's username, so signing them in
		// creates a separate account.
		directory := &directoryPlugin{users: map[string]*pluginv1.AuthenticateResponse{victim.Username + "\x00" + right: {
			ExternalSubject: "uid=" + env.name("signin-budget-directory"), Issuer: "ldaps://ldap.example.test",
			Username: victim.Username, Email: env.name("signin-budget-directory") + "@example.test",
			ManagedRole: pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_USER,
		}}}
		directoryID := PluginProviderID(env.installationID, "ldap")
		svc.SetPluginProviderSource(staticProviderSource{{
			Info: LoginProviderInfo{ID: directoryID, Mode: ProviderModeCredentials, InstallationID: env.installationID},
			Provider: NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: env.installationID, CapabilityID: "ldap", AutoProvision: true},
				sessions, env.resolver, func(context.Context) (pluginAuthClient, error) { return directory, nil }),
		}})
		defer svc.SetPluginProviderSource(nil)
		from := clientip.SetContext(t.Context(), "198.51.100.66")

		for label, signIn := range map[string]func() error{
			"a Jellyfin name with a '#'": func() error {
				_, _, _, err := svc.CompatLoginWithLocalFallback(from, victim.Username+"#x", right, "", "test", "", victim.Username)
				return err
			},
			"a directory identity of another account": func() error {
				_, _, err := svc.LoginWithProvider(from, directoryID, victim.Username, right, "test", "")
				return err
			},
		} {
			t.Run(label, func(t *testing.T) {
				defer limiter.expire()
				for range limiter.max - 1 {
					_, _, err := svc.LoginWithProvider(from, "", victim.Username, wrong, "test", "")
					wantInvalid(t, err)
				}
				if err := signIn(); err != nil {
					t.Fatalf("sign-in to the other account: %v", err)
				}
				_, _, err := svc.LoginWithProvider(from, "", victim.Username, wrong, "test", "")
				wantThrottled(t, err)
			})
		}
	})

	// An account kept from before the single login identity space can have
	// another account's username as its email. The name signs in to the
	// account with that username, so only that account's sign-ins make an
	// address known for it.
	t.Run("a legacy duplicate identifier is not an account's own name", func(t *testing.T) {
		defer limiter.expire()
		ctx := t.Context()
		victim := env.localAccount(t, "signin-budget-legacy-victim", models.RoleUser)
		holder := env.name("signin-budget-legacy-holder")
		tx, err := env.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
			t.Skipf("cannot bypass triggers to seed a legacy collision: %v", err)
		}
		var holderID int
		if err := tx.QueryRow(ctx, `INSERT INTO users
			(username, email, password_hash, role, permissions, local_password_login_enabled, access_group_id)
			VALUES ($1, $2, $3, 'user', $4, true, (SELECT id FROM access_groups WHERE is_default)) RETURNING id`,
			holder, victim.Username, externalSignInPasswordHash, DefaultUserPermissions()).Scan(&holderID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_login_identifiers (identifier, user_id, legacy_duplicate)
			VALUES ($1, $2, false), ($3, $2, true)`, holder, holderID, victim.Username); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		from := func(ip string) context.Context { return clientip.SetContext(ctx, ip) }
		victimHome, signedIn, refreshed := "198.51.100.78", "198.51.100.77", "203.0.113.77"
		if _, _, err := svc.LoginWithProvider(from(victimHome), "", victim.Username, right, "test", ""); err != nil {
			t.Fatalf("victim sign-in: %v", err)
		}
		pair, _, err := svc.LoginWithProvider(from(signedIn), "", holder, right, "test", "")
		if err != nil {
			t.Fatalf("holder sign-in: %v", err)
		}
		if _, err := svc.Refresh(from(refreshed), pair.RefreshToken); err != nil {
			t.Fatalf("holder refresh: %v", err)
		}
		for range limiter.max {
			_, _, err := svc.LoginWithProvider(from("192.0.2.77"), "", victim.Username, wrong, "test", "")
			wantInvalid(t, err)
		}
		for _, ip := range []string{signedIn, refreshed} {
			_, _, err := svc.LoginWithProvider(from(ip), "", victim.Username, wrong, "test", "")
			wantThrottled(t, err)
		}
		if _, _, err := svc.LoginWithProvider(from(victimHome), "", victim.Username, right, "test", ""); err != nil {
			t.Fatalf("name locked: victim sign-in from home: %v, want it to succeed", err)
		}
	})

	// A stranger who keeps a name locked does not lock its account out of the
	// addresses it signs in from: a sign-in or a refresh makes the address
	// known for the account's username and email, and a known address then
	// spends a budget of its own. Every other address stays locked.
	t.Run("a known address keeps signing in while the name is locked", func(t *testing.T) {
		defer limiter.expire()
		home, travel, stranger := "198.51.100.7", "203.0.113.20", "192.0.2.99"
		from := func(ip string) context.Context { return clientip.SetContext(t.Context(), ip) }
		signInFrom := func(ip, name, password string) (*TokenPair, error) {
			pair, _, err := svc.LoginWithProvider(from(ip), "", name, password, "test", ip)
			return pair, err
		}

		pair, err := signInFrom(home, user.Username, right)
		if err != nil {
			t.Fatalf("sign-in from home: %v", err)
		}
		for _, name := range []string{user.Username, user.Email} {
			for range limiter.max {
				if _, err := signInFrom(stranger, name, wrong); !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("stranger's guess at %s: err = %v, want ErrInvalidCredentials", name, err)
				}
			}
			if _, err := signInFrom(stranger, name, right); err == nil {
				t.Fatalf("%s locked: the stranger's address signed in", name)
			}
			if _, err := signInFrom(home, name, right); err != nil {
				t.Fatalf("%s locked: sign-in from home: %v, want it to succeed", name, err)
			}
		}

		// The known address's own budget bounds guessing from it too.
		for range limiter.max {
			if _, err := signInFrom(home, user.Username, wrong); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("guess from home: err = %v, want ErrInvalidCredentials", err)
			}
		}
		_, err = signInFrom(home, user.Username, right)
		wantThrottled(t, err)

		// Once the name's lock has passed, a success from home clears home's
		// own count too, so locking the name again does not shut home out.
		nameKey, _ := svc.signInNameKeys(t.Context(), user.Username)
		delete(limiter.counts, nameKey[0])
		if _, err := signInFrom(home, user.Username, right); err != nil {
			t.Fatalf("sign-in from home after the name's lock passed: %v", err)
		}
		for range limiter.max {
			if _, err := signInFrom(stranger, user.Username, wrong); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("stranger's guess: err = %v, want ErrInvalidCredentials", err)
			}
		}
		if _, err := signInFrom(home, user.Username, right); err != nil {
			t.Fatalf("name locked again: sign-in from home: %v, want it to succeed", err)
		}

		// A refresh makes its address known as well.
		_, err = signInFrom(travel, user.Username, right)
		wantThrottled(t, err)
		if _, err := svc.Refresh(from(travel), pair.RefreshToken); err != nil {
			t.Fatalf("refresh from travel: %v", err)
		}
		if _, err := signInFrom(travel, user.Username, right); err != nil {
			t.Fatalf("sign-in from travel after a refresh there: %v", err)
		}
	})
}

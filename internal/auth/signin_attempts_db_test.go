package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// countingSignInAttempts allows max attempts per key, then refuses until the
// key is reset or its window passes (expire), like the ratelimit
// AttemptLimiter without a clock. This package cannot import ratelimit.
type countingSignInAttempts struct {
	max    int
	counts map[string]int
}

func (c *countingSignInAttempts) Reserve(_ context.Context, key string) (time.Duration, bool) {
	if c.counts[key] >= c.max {
		return 7 * time.Minute, false
	}
	c.counts[key]++
	return 0, true
}

func (c *countingSignInAttempts) Reset(_ context.Context, key string) { delete(c.counts, key) }

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
	limiter := &countingSignInAttempts{max: 3, counts: map[string]int{}}
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
}

package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestDemoLoginDB: demo sign-in opens a session only on a public demo (demo
// mode on and demo.username naming an account a visitor may use), and that
// session is the ordinary login session a password sign-in opens, so every
// existing demo-mode rule applies to it. Catches demo sign-in working on a
// server that is not a demo, signing visitors in to an administrator or a
// disabled account, or opening a session of another kind.
func TestDemoLoginDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, demomode.SettingKey, demomode.UsernameKey))
	users, sessions := NewUserRepository(env.pool), NewSessionRepository(env.pool)
	jwt := NewJWTService("demo-login-db-test-secret-0123456789", 15*time.Minute, 24*time.Hour)
	svc := NewService(nil, jwt, sessions, users, nil, poolSettings{env}, nil)
	visitor := env.localAccount(t, "demo-visitor", models.RoleUser)
	admin := env.localAccount(t, "demo-admin", models.RoleAdmin)
	disabled := env.localAccount(t, "demo-disabled", models.RoleUser)
	if _, err := env.pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, enabled, username string
		want                    error
	}{
		{name: "demo mode off", enabled: "false", username: visitor.Username, want: ErrDemoLoginDisabled},
		{name: "no account named", enabled: demomode.OnValue, username: " ", want: ErrDemoLoginNotConfigured},
		{name: "missing account", enabled: demomode.OnValue, username: "no-such-demo-account-" + env.suffix, want: ErrDemoLoginNotConfigured},
		{name: "administrator", enabled: demomode.OnValue, username: admin.Username, want: ErrDemoLoginNotConfigured},
		{name: "disabled account", enabled: demomode.OnValue, username: disabled.Username, want: ErrDemoLoginNotConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.setSetting(t, demomode.SettingKey, tc.enabled)
			env.setSetting(t, demomode.UsernameKey, tc.username)
			if _, _, err := svc.DemoLogin(ctx, "Roku", "198.51.100.7"); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("public demo", func(t *testing.T) {
		env.setSetting(t, demomode.SettingKey, demomode.OnValue)
		env.setSetting(t, demomode.UsernameKey, "  "+strings.ToUpper(visitor.Username)+"  ")
		pair, user, err := svc.DemoLogin(ctx, "Roku", "198.51.100.7")
		if err != nil {
			t.Fatalf("DemoLogin: %v", err)
		}
		if user.ID != visitor.ID {
			t.Fatalf("signed in to account %d, want the demo account %d", user.ID, visitor.ID)
		}
		claims, err := jwt.ValidateToken(pair.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		session, err := sessions.GetByID(ctx, claims.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if session.UserID != visitor.ID || session.RevokedAt != nil || (session.Purpose != "" && session.Purpose != models.SessionPurposeAccount) ||
			session.AbsoluteExpiresAt != nil || session.DeviceName != "Roku" {
			t.Fatalf("session = %+v, want an ordinary login session of the demo account", session)
		}
		if _, err := svc.Refresh(ctx, pair.RefreshToken); err != nil {
			t.Fatalf("refreshing the demo session: %v", err)
		}
	})
}

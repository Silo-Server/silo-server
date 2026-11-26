package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A key session the switch-off sweep missed (a database written by hand: the
// setting, or demo.enabled, stored without the settings write) ends at its
// next refresh, through the real routes: the refresh is refused and the row
// revoked, its access token is refused from then on, and the same refresh
// token is still refused once the switch is back on. Before that refresh
// its access token keeps working, the limit the docs state.
func TestKeySessionSweepMissedEndsAtRefreshDB(t *testing.T) {
	d := newReachDB(t)
	ctx := t.Context()
	for _, key := range []string{config.AuthAppKeySignInSettingKey, demomode.SettingKey} {
		var saved *string
		_ = d.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&saved)
		t.Cleanup(func() {
			if saved == nil {
				_, _ = d.pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, key)
			} else {
				_, _ = d.pool.Exec(context.Background(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, *saved)
			}
		})
	}
	set := func(t *testing.T, key, value string) {
		t.Helper()
		if _, err := d.pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	// The real token service, middleware and refresh handler over the
	// stored sessions.
	users, sessions := auth.NewUserRepository(d.pool), auth.NewSessionRepository(d.pool)
	jwt := auth.NewJWTService("synthetic-key-switch-secret", time.Hour, 24*time.Hour)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)
	deps := d.deps
	deps.Auth = apimw.NewAuthMiddleware(jwt, sessions, fakeAPIKeys{}, fakeUsers{d.users})
	deps.Sessions = handlers.NewAuthHandler(svc, jwt, nil)
	h := newTestHandler(t, deps)

	for _, off := range []struct{ name, key, offValue, onValue string }{
		{"setting off", config.AuthAppKeySignInSettingKey, "false", "true"},
		{"demo mode on", demomode.SettingKey, demomode.OnValue, ""},
	} {
		t.Run(off.name, func(t *testing.T) {
			set(t, config.AuthAppKeySignInSettingKey, "true")
			set(t, demomode.SettingKey, "")
			userID := d.account(t)
			sessionID := uuid.NewString()
			d.store(t, userID, reachSession{id: sessionID, opened: models.SessionOpenedAppKey, keyVia: models.AppKeyEnrolledSession})
			access, err := jwt.GenerateAccessToken(userID, models.RoleUser, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			refresh, err := jwt.GenerateRefreshToken(userID, models.RoleUser, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			read := func() int {
				return do(t, h, http.MethodGet, Prefix+"/auth/sessions", "", map[string]string{"Authorization": "Bearer " + access}).Code
			}
			renew := func() int {
				body, _ := json.Marshal(map[string]string{"refresh_token": refresh})
				return do(t, h, http.MethodPost, Prefix+"/auth/refresh", string(body), map[string]string{"Content-Type": "application/json"}).Code
			}
			if got := renew(); got != http.StatusOK {
				t.Fatalf("refresh with the switch on = %d", got)
			}

			// Off without the sweep: the access token works until the
			// session next refreshes.
			set(t, off.key, off.offValue)
			if got := read(); got != http.StatusOK {
				t.Fatalf("read before the refresh = %d, want 200 (the stated limit)", got)
			}
			if got := renew(); got != http.StatusUnauthorized {
				t.Fatalf("refresh with the switch off = %d, want 401", got)
			}
			var revoked bool
			if err := d.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM auth_sessions WHERE id = $1`, sessionID).Scan(&revoked); err != nil || !revoked {
				t.Fatalf("session revoked = %v (%v), want ended", revoked, err)
			}
			if got := read(); got != http.StatusUnauthorized {
				t.Fatalf("read after the refresh = %d, want 401", got)
			}
			set(t, off.key, off.onValue)
			if got := renew(); got != http.StatusUnauthorized {
				t.Fatalf("refresh after the switch came back on = %d, want 401", got)
			}
		})
	}
}

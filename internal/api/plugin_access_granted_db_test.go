package api

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestPluginAccessTreatsAnotherAppAsNoSessionDB: the plugin-content proxy is
// outside the operation registry and its origin gate, so the resolver itself
// answers a session another app's key opened as a caller with no session,
// whether its token comes as the bearer header, the token query, or a launch
// cookie minted from it; the plugin is then never told the account, role or
// profile. Through the real session lookup: a password session and a device
// signed in by code resolve as before. Catches the resolver authenticating
// such a session, which let it reach a plugin's authenticated routes.
func TestPluginAccessTreatsAnotherAppAsNoSessionDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := "pluginapp-" + uuid.NewString()[:8]
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions)
		VALUES ($1, $2, 'x', 'user', '{}') RETURNING id`, name, name+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, userID) })
	publicKey := make([]byte, 65)
	if _, err := rand.Read(publicKey); err != nil {
		t.Fatal(err)
	}
	grantKey := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
		VALUES ($1::uuid, $2, $3, $4, 'app', 'Other app', $5)`, grantKey, userID, publicKey, "key-"+grantKey, models.AppKeyEnrolledGrant); err != nil {
		t.Fatal(err)
	}
	session := func(openedVia string, deviceKey *string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
			VALUES ($1, $2, 'test', $3, $4, $5::uuid)`, id, userID, time.Now().Add(time.Hour), openedVia, deviceKey); err != nil {
			t.Fatal(err)
		}
		return id
	}
	jwt := auth.NewJWTService("plugin-access-granted-secret-0123456789", time.Hour, time.Hour)
	resolver := &pluginAccessResolver{jwt: jwt, sessions: auth.NewSessionRepository(pool), noRenewal: true}
	for _, tc := range []struct {
		name      string
		session   string
		wantAuthn bool
	}{
		{"another app's key", session(models.SessionOpenedAppKey, &grantKey), false},
		{"password", session(models.SessionOpenedPassword, nil), true},
		{"device signed in by code", session(models.SessionOpenedDeviceLogin, nil), true},
	} {
		access, err := jwt.GenerateAccessToken(userID, "user", tc.session)
		if err != nil {
			t.Fatal(err)
		}
		launch, err := jwt.GeneratePluginAccessToken(userID, "user", tc.session, "", nil, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		requests := map[string]func() pluginAccess{
			"bearer": func() pluginAccess {
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Set("Authorization", "Bearer "+access)
				return resolver.resolve(r)
			},
			"query token": func() pluginAccess {
				return resolver.resolve(httptest.NewRequest("GET", "/?token="+access, nil))
			},
			"launch cookie": func() pluginAccess {
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Set("Cookie", auth.PluginAccessCookieName+"="+launch)
				return resolver.resolve(r)
			},
		}
		for form, resolve := range requests {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				got := resolve()
				if got.authenticated != tc.wantAuthn {
					t.Fatalf("resolved %+v, want authenticated %v", got, tc.wantAuthn)
				}
				if !tc.wantAuthn && (got.userID != 0 || got.profileID != "" || got.admin) {
					t.Fatalf("an unauthenticated answer names the account: %+v", got)
				}
			})
		}
	}
}

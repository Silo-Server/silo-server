package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestRoleChangeForcesTokenRefreshPostgres walks a demotion against the real
// session repository: the admin's access token is refused with
// token_refresh_required, the refresh token still works and issues a token
// with the new role, and the refreshed token no longer passes the admin gate.
func TestRoleChangeForcesTokenRefreshPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	jwt := auth.NewJWTService("role-change-secret-0123456789abcdef", time.Hour, time.Hour)
	sessions := auth.NewSessionRepository(pool)
	users := auth.NewUserRepository(pool)
	service := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)
	am := NewAuthMiddleware(jwt, sessions, nil, nil)

	seedUser := func(t *testing.T, role string) int {
		t.Helper()
		name := fmt.Sprintf("role-change-%s-%d", role, time.Now().UnixNano())
		var id int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', $3) RETURNING id`,
			name, name+"@example.test", role,
		).Scan(&id); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	seedSession := func(t *testing.T, userID int, impersonator *int) string {
		t.Helper()
		session := models.AuthSession{
			ID:                 fmt.Sprintf("role-change-session-%d-%d", userID, time.Now().UnixNano()),
			UserID:             userID,
			ExpiresAt:          time.Now().Add(time.Hour),
			ImpersonatorUserID: impersonator,
		}
		if impersonator != nil {
			started := time.Now()
			session.ImpersonationStartedAt = &started
		}
		if err := sessions.Create(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
		return session.ID
	}
	serve := func(t *testing.T, h http.Handler, token string) (*reasonWriter, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := newReasonWriter()
		h.ServeHTTP(rec, req)
		return rec, rec.reason
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	adminRoute := am.RequireAuth(RequireAdmin(ok))
	authenticatedRoute := am.RequireAuth(ok)

	t.Run("demoted admin", func(t *testing.T) {
		adminID := seedUser(t, models.RoleAdmin)
		sessionID := seedSession(t, adminID, nil)
		access, err := jwt.GenerateAccessToken(adminID, models.RoleAdmin, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		refresh, err := jwt.GenerateRefreshToken(adminID, models.RoleAdmin, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if rec, _ := serve(t, adminRoute, access); rec.Code != http.StatusNoContent {
			t.Fatalf("admin before demotion: status %d", rec.Code)
		}

		if err := users.Update(ctx, adminID, models.UpdateUserInput{Role: new(models.RoleUser)}); err != nil {
			t.Fatalf("demote: %v", err)
		}
		if rec, reason := serve(t, adminRoute, access); rec.Code != http.StatusUnauthorized || reason != ReasonTokenRefreshRequired {
			t.Fatalf("stale admin token: status %d reason %q, want 401 %q", rec.Code, reason, ReasonTokenRefreshRequired)
		}

		pair, err := service.Refresh(ctx, refresh)
		if err != nil {
			t.Fatalf("refresh after demotion: %v", err)
		}
		claims, err := jwt.ValidateToken(pair.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		if claims.Role != models.RoleUser || claims.SessionID != sessionID {
			t.Fatalf("refreshed claims role %q session %q, want %q on the same session", claims.Role, claims.SessionID, models.RoleUser)
		}
		if rec, _ := serve(t, authenticatedRoute, pair.AccessToken); rec.Code != http.StatusNoContent {
			t.Fatalf("refreshed token on an ordinary route: status %d", rec.Code)
		}
		if rec, _ := serve(t, adminRoute, pair.AccessToken); rec.Code != http.StatusForbidden {
			t.Fatalf("refreshed token on an admin route: status %d, want 403", rec.Code)
		}
	})

	t.Run("impersonation token", func(t *testing.T) {
		ownerID := seedUser(t, models.RoleAdmin)
		targetID := seedUser(t, models.RoleUser)
		sessionID := seedSession(t, targetID, &ownerID)
		access, err := jwt.GenerateAccessToken(targetID, models.RoleUser, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		// The session belongs to the account being viewed as, so its role is
		// the one compared; the impersonator's admin role is not.
		if rec, reason := serve(t, authenticatedRoute, access); rec.Code != http.StatusNoContent {
			t.Fatalf("impersonation token: status %d reason %q", rec.Code, reason)
		}
	})
}

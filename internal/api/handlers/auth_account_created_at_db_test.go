package handlers

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// createdAtEnv is a database with accounts whose creation times are set
// apart, and the auth service the handlers call.
type createdAtEnv struct {
	pool     *pgxpool.Pool
	jwt      *auth.JWTService
	svc      *auth.Service
	sessions *auth.SessionRepository
	suffix   string
}

func newCreatedAtEnv(t *testing.T) *createdAtEnv {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e := &createdAtEnv{pool: pool, suffix: fmt.Sprintf("createdat%d", time.Now().UnixNano()),
		jwt: auth.NewJWTService("created-at-test-jwt-secret", time.Hour, 24*time.Hour), sessions: auth.NewSessionRepository(pool)}
	e.svc = auth.NewService(nil, e.jwt, e.sessions, auth.NewUserRepository(pool), nil, nil, nil)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE username LIKE $1`, "%"+e.suffix)
	})
	return e
}

// account inserts an account made at created (stored to the microsecond)
// and returns its ID.
func (e *createdAtEnv) account(t *testing.T, label, role string, created time.Time) int {
	t.Helper()
	var id int
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, permissions, created_at)
		VALUES ($1, $2, 'x', $3, '{}', $4) RETURNING id`, label+"-"+e.suffix, label+"-"+e.suffix+"@example.test", role, created).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// keySession opens a session of userID through a device key enrolled the
// way via says, and returns its refresh token.
func (e *createdAtEnv) keySession(t *testing.T, userID int, via, kind string) string {
	t.Helper()
	public := make([]byte, 65)
	if _, err := rand.Read(public); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
		VALUES ($1::uuid, $2, $3, $4, $5, 'Key', $6)`, key, userID, public, "key-"+key, kind, via); err != nil {
		t.Fatal(err)
	}
	session := models.AuthSession{ID: uuid.NewString(), UserID: userID, DeviceName: "key",
		ExpiresAt: time.Now().Add(time.Hour), DeviceKeyID: &key, OpenedVia: models.SessionOpenedAppKey}
	if err := e.sessions.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	refresh, err := e.jwt.GenerateRefreshToken(userID, models.RoleUser, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return refresh
}

// The refresh answer carries the account's creation time for the person's
// own sessions, equal to the row at microsecond precision, and none for a
// session another app's key opened, as that app's sealed replies carry
// none. Catches the handler giving the time to such a session.
func TestRefreshAccountCreatedAtDB(t *testing.T) {
	e := newCreatedAtEnv(t)
	created := time.Date(2024, 2, 29, 23, 59, 58, 123456000, time.UTC)
	user := e.account(t, "refresh", models.RoleUser, created)
	h := NewAuthHandler(e.svc, e.jwt, nil)

	own, err := h.Refresh(t.Context(), e.keySession(t, user, models.AppKeyEnrolledSession, models.DeviceKeyKindDevice))
	if err != nil || !own.AccountCreatedAt.Equal(created) {
		t.Fatalf("the person's own key session: %v, %v; want %s", own.AccountCreatedAt, err, created.Format(time.RFC3339Nano))
	}
	app, err := h.Refresh(t.Context(), e.keySession(t, user, models.AppKeyEnrolledGrant, models.DeviceKeyKindApp))
	if err != nil || !app.AccountCreatedAt.IsZero() {
		t.Fatalf("another app's session: %v, %v; want none", app.AccountCreatedAt, err)
	}
}

// An administrator viewing as another account gets a pair whose user is
// the viewed account, and account_created_at describes that same account,
// never the administrator's. Catches the time following the session's
// impersonator.
func TestViewAsPairCarriesTheViewedAccountsCreatedAtDB(t *testing.T) {
	e := newCreatedAtEnv(t)
	adminCreated := time.Date(2023, 1, 2, 3, 4, 5, 600000000, time.UTC)
	viewedCreated := time.Date(2025, 6, 7, 8, 9, 10, 111213000, time.UTC)
	admin := e.account(t, "viewas-admin", models.RoleAdmin, adminCreated)
	viewed := e.account(t, "viewas-viewed", models.RoleUser, viewedCreated)
	session := models.AuthSession{ID: uuid.NewString(), UserID: admin, DeviceName: "browser",
		ExpiresAt: time.Now().Add(time.Hour), OpenedVia: models.SessionOpenedPassword}
	if err := e.sessions.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	h := &AdminHandler{ImpersonationService: e.svc}
	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: admin, Role: models.RoleAdmin, SessionID: session.ID, TokenType: auth.TokenTypeAccess})
	view, err := h.ImpersonateAdminAccount(ctx, viewed, "browser", "127.0.0.1")
	if err != nil {
		t.Fatalf("view as: %v", err)
	}
	if view.User.ID != viewed || !view.User.CreatedAt.Equal(viewedCreated) {
		t.Fatalf("view-as pair: user %d created %s; want %d created %s", view.User.ID,
			view.User.CreatedAt.Format(time.RFC3339Nano), viewed, viewedCreated.Format(time.RFC3339Nano))
	}
}

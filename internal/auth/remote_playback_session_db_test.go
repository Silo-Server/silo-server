package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

const remoteTestRefreshExpiry = 30 * 24 * time.Hour

type remotePlaybackEnv struct {
	pool      *pgxpool.Pool
	jwt       *JWTService
	svc       *Service
	devices   *DeviceLoginService
	user      *models.User
	profileID string
	// approver is the admin's own login session that approves the handoff.
	approver *Claims
	name     string
}

// newRemotePlaybackEnv builds an admin account with one profile and its own
// login session, on the migrated database named by SILO_TEST_DATABASE_URL.
func newRemotePlaybackEnv(t *testing.T) *remotePlaybackEnv {
	t.Helper()
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

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	env := &remotePlaybackEnv{pool: pool, name: "remote-playback-test-" + suffix}
	users := NewUserRepository(pool)
	sessions := NewSessionRepository(pool)
	env.jwt = NewJWTService("remote-playback-test-secret", time.Hour, remoteTestRefreshExpiry)
	env.svc = NewService(NewLocalProvider(users, sessions), env.jwt, sessions, users, nil, nil, nil)
	stores := pgstore.NewPostgresProvider(pool)
	env.devices = NewDeviceLoginService(pool, users, env.jwt, sessions, stores, access.NewProfileTokenService("remote-playback-profile-secret", time.Hour))

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'admin', true) RETURNING id`, env.name, env.name+"@example.invalid").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, `DELETE FROM device_login_requests WHERE device_name LIKE $1`, env.name+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM auth_sessions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
	})
	if env.user, err = users.GetByID(ctx, userID); err != nil {
		t.Fatal(err)
	}
	store, err := stores.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	env.profileID = "remote-profile-" + suffix
	if err := store.CreateProfile(ctx, userstore.Profile{ID: env.profileID, Name: "Remote", IsPrimary: true}); err != nil {
		t.Fatal(err)
	}

	approver := models.AuthSession{ID: uuid.New().String(), UserID: userID, DeviceName: env.name + "-phone", ExpiresAt: time.Now().Add(remoteTestRefreshExpiry)}
	if err := sessions.Create(ctx, approver); err != nil {
		t.Fatal(err)
	}
	env.approver = &Claims{UserID: userID, Role: env.user.Role, SessionID: approver.ID, TokenType: TokenTypeAccess}
	return env
}

// handoff runs start, approve-handoff and poll, and returns the collected
// result.
func (e *remotePlaybackEnv) handoff(t *testing.T) *DeviceLoginPollResult {
	t.Helper()
	ctx := t.Context()
	start, err := e.devices.Start(ctx, DeviceLoginStartInput{DeviceName: e.name + "-tv", ClientPurpose: DeviceLoginPurposeRemote, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.devices.ApproveForProfile(WithClaims(ctx, e.approver), DeviceLoginLookupInput{UserCode: start.UserCode}, e.user.ID, e.profileID); err != nil {
		t.Fatal(err)
	}
	poll, err := e.devices.Poll(ctx, start.DeviceCode)
	if err != nil || poll.TokenPair == nil {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	return poll
}

func (e *remotePlaybackEnv) sessionRow(t *testing.T, id string) *models.AuthSession {
	t.Helper()
	session, err := e.svc.sessions.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// TestRemotePlaybackRefreshKeepsHandoffCapDB: refreshing a handoff session
// never moves it, or its tokens, past the 24 hours the approver granted.
func TestRemotePlaybackRefreshKeepsHandoffCapDB(t *testing.T) {
	env := newRemotePlaybackEnv(t)
	ctx := t.Context()
	poll := env.handoff(t)
	if poll.TokenPair.RefreshToken == "" {
		t.Fatal("handoff collected no refresh token")
	}
	access, err := env.jwt.ValidateToken(poll.TokenPair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	limit := poll.SessionExpiresAt
	if limit.After(time.Now().Add(remotePlaybackSessionTTL)) {
		t.Fatalf("session_expires_at %v is past the handoff lifetime", limit)
	}
	session := env.sessionRow(t, access.SessionID)
	if session.AbsoluteExpiresAt == nil || !session.AbsoluteExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("handoff session records no absolute limit: %+v", session)
	}
	if d := session.AbsoluteExpiresAt.Sub(limit); d > time.Millisecond || d < -time.Millisecond {
		t.Fatalf("stored limit %v differs from session_expires_at %v", *session.AbsoluteExpiresAt, limit)
	}
	if session.Purpose != models.SessionPurposeRemotePlayback || session.BoundProfileID != env.profileID {
		t.Fatalf("handoff session is not bound: %+v", session)
	}

	refreshed, err := env.svc.Refresh(ctx, poll.TokenPair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.RefreshToken == "" {
		t.Fatal("refresh dropped the refresh token")
	}
	if after := env.sessionRow(t, access.SessionID); after.ExpiresAt.After(*session.AbsoluteExpiresAt) {
		t.Fatalf("refresh moved the handoff session to %v, past its limit %v", after.ExpiresAt, *session.AbsoluteExpiresAt)
	}
	for _, token := range []string{refreshed.AccessToken, refreshed.RefreshToken} {
		claims, err := env.jwt.ValidateToken(token)
		if err != nil {
			t.Fatal(err)
		}
		if claims.ExpiresAt.After(*session.AbsoluteExpiresAt) {
			t.Fatalf("%s token expires %v, past the handoff limit %v", claims.TokenType, claims.ExpiresAt.Time, *session.AbsoluteExpiresAt)
		}
	}

	// Once the limit passes, the refresh token is refused.
	if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET expires_at = NOW() - INTERVAL '1 second',
		absolute_expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, access.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Refresh(ctx, refreshed.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh after the handoff limit = %v, want ErrSessionRevoked", err)
	}
}

// TestRemotePlaybackSessionCannotApproveDeviceLoginDB: a handoff session is
// not the account's own login session, so it cannot approve a new ordinary
// device login, even with a token that does not say what it is.
func TestRemotePlaybackSessionCannotApproveDeviceLoginDB(t *testing.T) {
	env := newRemotePlaybackEnv(t)
	ctx := t.Context()
	poll := env.handoff(t)
	claims, err := env.jwt.ValidateToken(poll.TokenPair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.IsOwnLoginSession() {
		t.Fatal("a handoff access token counts as the account's own login session")
	}
	bare := &Claims{UserID: env.user.ID, Role: env.user.Role, SessionID: claims.SessionID, TokenType: TokenTypeAccess}
	for name, approver := range map[string]*Claims{"handoff token": claims, "token without scope": bare} {
		start, err := env.devices.Start(ctx, DeviceLoginStartInput{DeviceName: env.name + "-second"})
		if err != nil {
			t.Fatal(err)
		}
		err = env.devices.Approve(WithClaims(ctx, approver), DeviceLoginLookupInput{UserCode: start.UserCode}, env.user.ID)
		if !errors.Is(err, ErrSessionRevoked) {
			t.Fatalf("%s approved an ordinary device login: %v", name, err)
		}
	}
}

package jellycompat

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestPersistentSessionStoreRejectsSharedRevocationAcrossIndependentPools(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	poolA, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect replica A pool: %v", err)
	}
	t.Cleanup(poolA.Close)
	poolB, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect replica B pool: %v", err)
	}
	t.Cleanup(poolB.Close)

	cipher, err := secret.New([]byte(strings.Repeat("authority-test-key", 2)))
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	users := auth.NewUserRepository(poolA)
	user, err := users.Create(t.Context(), models.CreateUserInput{
		Username: "compat-authority-" + suffix,
		Email:    "compat-authority-" + suffix + "@example.invalid",
		Password: "initial-password",
		Role:     models.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("create test account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = poolA.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
	})

	nativeSessionID := uuid.NewString()
	if err := auth.NewSessionRepository(poolA).Create(t.Context(), models.AuthSession{
		ID:           nativeSessionID,
		UserID:       user.ID,
		ExpiresAt:    time.Now().Add(time.Hour),
		AuthRevision: user.AuthRevision,
	}); err != nil {
		t.Fatalf("create native session: %v", err)
	}

	replicaA := NewPersistentSessionStore(time.Hour, time.Now, NewSessionRepository(poolA, cipher))
	replicaB := NewPersistentSessionStore(time.Hour, time.Now, NewSessionRepository(poolB, cipher))
	compatSession := Session{
		Token:                 uuid.NewString(),
		Username:              user.Username,
		AccountUsername:       user.Username,
		ProfileID:             "profile-1",
		ProfileName:           "Profile",
		PseudoUserID:          uuid.New(),
		StreamAppUserID:       user.ID,
		StreamAppSessionID:    nativeSessionID,
		AuthRevision:          user.AuthRevision,
		StreamAppAccessToken:  "access-token",
		StreamAppRefreshToken: "refresh-token",
		StreamAppTokenExpiry:  time.Now().Add(time.Hour),
	}
	if err := replicaA.Put(compatSession); err != nil {
		t.Fatalf("seed compat session through replica A: %v", err)
	}
	if _, ok := replicaB.Get(compatSession.Token); !ok {
		t.Fatal("replica B did not warm its independent cache")
	}

	if err := users.UpdateAndRevokeAuthority(t.Context(), user.ID, models.UpdateUserInput{
		Password: new("replacement-password"),
	}); err != nil {
		t.Fatalf("revoke account authority through replica A: %v", err)
	}
	if err := replicaB.Update(compatSession.Token, func(session *Session) error {
		session.ProfileName = "stale replica update"
		return nil
	}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("stale replica update error = %v, want ErrSessionNotFound", err)
	}
	if _, ok := replicaB.Get(compatSession.Token); ok {
		t.Fatal("replica B accepted its cached credential after shared revocation")
	}
	var persisted int
	if err := poolB.QueryRow(t.Context(), `SELECT COUNT(*) FROM jellycompat_sessions WHERE token = $1`, compatSession.Token).Scan(&persisted); err != nil {
		t.Fatalf("count persisted compat sessions: %v", err)
	}
	if persisted != 0 {
		t.Fatalf("persisted compat session count = %d, want 0", persisted)
	}
}

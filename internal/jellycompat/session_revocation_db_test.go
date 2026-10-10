package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// TestRevokedCompatSessionStaysDeletedPostgres: a replica that still caches
// a compat session after the revoking transaction deleted its row cannot
// write the row back, neither by extending the session nor by storing
// refreshed Silo tokens. Before the revocation both writes update the row.
func TestRevokedCompatSessionStaysDeletedPostgres(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	user, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jfrevoke-%d", suffix),
		Email: fmt.Sprintf("jfrevoke-%d@example.test", suffix), Password: "correct horse battery", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID) })
	cipher, err := secret.New([]byte("jellycompat-revoke-test-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewSessionRepository(pool, cipher)
	start := time.Now().UTC().Truncate(time.Microsecond)
	now := start
	store := NewPersistentSessionStore(24*time.Hour, func() time.Time { return now }, repo)
	extendToken, refreshToken := fmt.Sprintf("jfrevoke-extend-%d", suffix), fmt.Sprintf("jfrevoke-refresh-%d", suffix)
	for _, token := range []string{extendToken, refreshToken} {
		if err := store.Put(Session{Token: token, StreamAppUserID: user.ID, PseudoUserID: uuid.New(),
			StreamAppAccessToken: "access", StreamAppRefreshToken: "refresh", StreamAppTokenExpiry: start.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	setAccess := func(s *Session) error { s.StreamAppAccessToken = "new-access"; return nil }

	// Past half the TTL: Lookup extends both sessions to start+37h.
	now = start.Add(13 * time.Hour)
	for _, token := range []string{extendToken, refreshToken} {
		if _, err := store.Lookup(ctx, token); err != nil {
			t.Fatalf("live session %s: %v", token, err)
		}
	}
	if err := store.Update(refreshToken, setAccess); err != nil {
		t.Fatalf("storing refreshed tokens on a live session: %v", err)
	}
	stored, err := repo.GetByToken(ctx, refreshToken, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(24 * time.Hour); !stored.ExpiresAt.Equal(want) || stored.StreamAppAccessToken != "new-access" {
		t.Fatalf("live session row = expires %v access %q, want expires %v access new-access", stored.ExpiresAt, stored.StreamAppAccessToken, want)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.RevokeSignInsInTransaction(ctx, tx, user.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The store has not evicted the account. Past half the TTL again, so
	// Lookup tries to extend the cached copy.
	now = start.Add(26 * time.Hour)
	if _, err := store.Lookup(ctx, extendToken); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("extending a revoked session = %v, want ErrSessionNotFound", err)
	}
	if err := store.Update(refreshToken, setAccess); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("storing refreshed tokens on a revoked session = %v, want ErrSessionNotFound", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jellycompat_sessions WHERE streamapp_user_id = $1`, user.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("revoked account has %d stored compat sessions, want 0", rows)
	}
}

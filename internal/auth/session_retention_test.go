package auth

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSessionRepositoryDeleteExpiredAndListByUserPageDB runs against a
// migrated database: DeleteExpired removes every row past its expiry, revoked
// or not, and ListByUserPage lists only live rows, newest first, strictly
// after the key.
func TestSessionRepositoryDeleteExpiredAndListByUserPageDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true)
		RETURNING id`,
		"auth-session-retention-"+suffix, "auth-session-retention-"+suffix+"@example.invalid",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM auth_sessions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	repo := NewSessionRepository(pool)
	now := time.Now().UTC()
	insert := func(id string, createdAt, expiresAt time.Time, revoked bool) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth_sessions (id, user_id, device_name, created_at, expires_at, revoked_at)
			VALUES ($1, $2, 'fixture', $3, $4, CASE WHEN $5 THEN $3::timestamptz ELSE NULL END)`,
			id, userID, createdAt, expiresAt, revoked); err != nil {
			t.Fatalf("insert session %s: %v", id, err)
		}
	}
	insert(suffix+"-expired", now.Add(-3*time.Hour), now.Add(-time.Minute), false)
	insert(suffix+"-expired-revoked", now.Add(-4*time.Hour), now.Add(-time.Minute), true)
	insert(suffix+"-live-revoked", now.Add(-2*time.Hour), now.Add(time.Hour), true)
	insert(suffix+"-live-a", now.Add(-time.Hour), now.Add(time.Hour), false)
	insert(suffix+"-live-b", now.Add(-time.Hour), now.Add(time.Hour), false)
	insert(suffix+"-live-c", now.Add(-time.Minute), now.Add(time.Hour), false)

	page, err := repo.ListByUserPage(ctx, userID, nil, 2)
	if err != nil {
		t.Fatalf("ListByUserPage: %v", err)
	}
	if ids := sessionIDs(page); len(ids) != 2 || ids[0] != suffix+"-live-c" || ids[1] != suffix+"-live-b" {
		t.Fatalf("first page = %v", ids)
	}
	last := page[len(page)-1]
	page, err = repo.ListByUserPage(ctx, userID, &SessionKey{CreatedAt: last.CreatedAt, ID: last.ID}, 2)
	if err != nil {
		t.Fatalf("ListByUserPage after key: %v", err)
	}
	if ids := sessionIDs(page); len(ids) != 1 || ids[0] != suffix+"-live-a" {
		t.Fatalf("second page = %v", ids)
	}

	deleted, err := repo.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted < 2 {
		t.Fatalf("DeleteExpired deleted %d rows, want at least the 2 expired fixtures", deleted)
	}
	remaining, err := repo.ListByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if ids := sessionIDs(remaining); len(ids) != 4 {
		t.Fatalf("remaining = %v, want the four unexpired rows", ids)
	}
	for _, s := range remaining {
		if !s.ExpiresAt.After(now) {
			t.Fatalf("expired row %s survived DeleteExpired", s.ID)
		}
	}
}

func sessionIDs(sessions []*models.AuthSession) []string {
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

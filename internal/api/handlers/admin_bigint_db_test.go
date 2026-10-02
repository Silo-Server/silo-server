package handlers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAdminLastActivityAcceptsBigintUserFilter(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h := &AdminHandler{pool: pool}
	var wide int64 = 3_000_000_000
	activity, err := h.loadUserLastActiveAt(t.Context(), []int{int(wide)})
	if err != nil {
		t.Fatal(err)
	}
	if len(activity) != 0 {
		t.Fatalf("unexpected activity for test filter: %v", activity)
	}
}

// TestGetAdminAccountReportsLastActivity reads the account's latest
// activity_log row into last_active_at, as the v1 read and the list do.
func TestGetAdminAccountReportsLastActivity(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	const userID = 2_000_000_417
	at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	// activity_log.user_id references users, so the account must exist.
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, username, email, password_hash, role, enabled) VALUES ($1, 'last-activity-417', 'last-activity-417@example.invalid', '', 'user', true)`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO activity_log ("timestamp", client_ip, user_id, method, path) VALUES ($1, '192.0.2.1', $2, 'GET', '/'), ($3, '192.0.2.1', $2, 'GET', '/')`, at, userID, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM activity_log WHERE user_id = $1`, userID)
	})
	repo := &mutatingUserRepo{current: models.User{ID: userID, Role: models.RoleUser}}
	h := &AdminHandler{pool: pool, userRepo: repo}
	view, err := h.GetAdminAccount(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if view.User.LastActiveAt == nil || !view.User.LastActiveAt.Equal(at) {
		t.Fatalf("last_active_at = %v, want %v", view.User.LastActiveAt, at)
	}
}

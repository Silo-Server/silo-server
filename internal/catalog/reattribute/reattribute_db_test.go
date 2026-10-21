package reattribute

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type testEnv struct {
	pool      *pgxpool.Pool
	userID    int
	profileID string
	suffix    int64
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
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

	suffix := time.Now().UnixNano()
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("reattr-user-%d", suffix),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	env := &testEnv{
		pool:      pool,
		userID:    userID,
		profileID: fmt.Sprintf("00000000-0000-4000-8000-%012d", suffix%1_000_000_000_000),
		suffix:    suffix,
	}
	t.Cleanup(func() {
		for _, table := range []string{
			"admin_playback_history", "user_downloads", "downloads", "user_watch_history",
			"user_watch_progress", "user_favorites", "user_watchlist", "user_ratings",
			"user_audio_preferences", "user_subtitle_preferences", "user_series_playback_preferences",
			"user_home_item_dismissals",
		} {
			_, _ = pool.Exec(ctx, `DELETE FROM `+table+` WHERE user_id = $1`, userID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	return env
}

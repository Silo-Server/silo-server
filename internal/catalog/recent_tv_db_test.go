package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The home row's newest TV cards cost one round trip per statement once the
// connection has prepared them: the JIT guard rides in the cutoff's and the
// window's batches, and the first window answers without the full query.
func TestRecentTVListNewestRoundTrips(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// One connection, so the repeated read below reuses its prepared statements.
	cfg.MaxConns = 1
	var writes atomic.Int64
	countServerWrites(cfg, &writes)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("recent-tv-round-trips-%d", time.Now().UnixNano())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, prefix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folder)
	})
	// Twelve shows with one arrival each fill the limit-2 first window (12 rows).
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range 12 {
		series, episode := fmt.Sprintf("%s-s%02d", prefix, i), fmt.Sprintf("%s-s%02d-e1", prefix, i)
		at := base.Add(time.Duration(i) * time.Hour)
		exec(`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', $1, 'matched', '{}')`, series)
		exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, series, folder, at)
		exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Episode')`, episode, series)
		exec(`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, episode, folder, at)
		exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path) VALUES ($1, $2, $3)`, episode, folder, "/"+episode)
	}

	repo := NewRecentTVRepository(pool)
	full, _, _, err := repo.List(ctx, RecentTVQuery{LibraryIDs: []int{folder}, Limit: 3, UniqueTargets: true, SkipTotal: true})
	if err != nil {
		t.Fatal(err)
	}
	q := RecentTVQuery{LibraryIDs: []int{folder}, Limit: 2}
	if _, _, err := repo.ListNewest(ctx, q); err != nil {
		t.Fatal(err)
	}
	writes.Store(0)
	got, hasMore, err := repo.ListNewest(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if n := writes.Load(); n != 2 {
		t.Fatalf("ListNewest took %d round trips on a warm connection, want 2: the cutoff and one window", n)
	}
	if !reflect.DeepEqual(got, full[:2]) || !hasMore {
		t.Fatalf("ListNewest = %+v (more %v), want %+v (more true)", got, hasMore, full[:2])
	}
}

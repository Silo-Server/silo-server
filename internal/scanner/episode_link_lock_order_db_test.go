package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Curated rebind, rename and split lock a series' files in id order. Bulk
// episode linking must take the same order, or the two deadlock when the
// files' id order differs from the order the link statement visits them.
func TestBulkLinkEpisodesLocksFilesInIDOrderPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	seriesID := fmt.Sprintf("bulk-link-order-%d", time.Now().UnixNano())
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', 'Bulk link order', true) RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', 'Bulk link order', 'matched', '{}'::text[])`, seriesID); err != nil {
		t.Fatal(err)
	}
	var base int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) + 1000 FROM media_files`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	// Episode 1 holds the highest file id and is inserted first, so heap,
	// index and episode order all visit the files in descending id order.
	ids := make([]int64, 3)
	for episode := 1; episode <= 3; episode++ {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, $3, 'Ep')`,
			fmt.Sprintf("%s-e%d", seriesID, episode), seriesID, episode); err != nil {
			t.Fatal(err)
		}
		ids[episode-1] = base + int64(4-episode)
		if _, err := pool.Exec(ctx, `INSERT INTO media_files (id, content_id, media_folder_id, file_path, file_size, season_number, episode_number) VALUES ($1, $2, $3, $4, 1024, 1, $5)`,
			ids[episode-1], seriesID, folderID, fmt.Sprintf("/synthetic/%s/e%d.mkv", seriesID, episode), episode); err != nil {
			t.Fatal(err)
		}
	}
	middle := ids[1]

	// Hold the middle file so each writer has taken its first file and waits.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var blockerPID int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `SELECT 1 FROM media_files WHERE id = $1 FOR UPDATE`, middle); err != nil {
		t.Fatal(err)
	}

	curation, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = curation.Rollback(context.Background()) }()
	var curationPID int
	if err := curation.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&curationPID); err != nil {
		t.Fatal(err)
	}
	curationDone := make(chan error, 1)
	go func() {
		ctx := access.WithMetadataCurationAuthorizer(ctx, func(ctx context.Context, _ []int) error { return ctx.Err() })
		if err := catalog.LockMetadataCurationFiles(ctx, curation, seriesID); err != nil {
			curationDone <- err
			return
		}
		curationDone <- curation.Commit(ctx)
	}()
	waitForBlocked(t, ctx, pool, `pid = $1`, curationPID)

	linkDone := make(chan error, 1)
	go func() {
		_, err := NewFileRepository(pool).BulkLinkEpisodesBySeries(ctx, seriesID)
		linkDone <- err
	}()
	waitForBlocked(t, ctx, pool, `pid NOT IN ($1, $2)
		AND pg_blocking_pids(pid) && ARRAY[$1, $2]::int[]`, curationPID, blockerPID)

	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-curationDone; err != nil {
		t.Errorf("curation file lock: %v", err)
	}
	if err := <-linkDone; err != nil {
		t.Errorf("bulk episode linking: %v", err)
	}
}

// waitForBlocked returns once a backend matching where waits on a lock.
func waitForBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, where string, args ...any) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE cardinality(pg_blocking_pids(pid)) > 0 AND `+where+`)`, args...).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFolderMembershipReconciliationRetriesDeadlockDB covers the scan step
// that removes stale library memberships. A metadata or match writer updating
// the same rows can make Postgres abort that transaction with a deadlock
// (40P01). The transaction is rolled back, so reconciliation must rerun it
// instead of failing the scan.
//
// A trigger stands in for the concurrent writer: it raises 40P01 the first
// time each reconciliation deletes one of this test's memberships. Its
// sequence counts attempts, and sequences are not rolled back with the
// aborted transaction.
func TestFolderMembershipReconciliationRetriesDeadlockDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	withFastDeadlockRetry(t, 5)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	suffix := time.Now().UnixNano()
	movieID := fmt.Sprintf("mdr-movie-%d", suffix)
	seriesID := fmt.Sprintf("mdr-series-%d", suffix)
	episodeID := fmt.Sprintf("mdr-episode-%d", suffix)

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('movies', 'Deadlock Retry Test', true) RETURNING id`,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM episode_libraries WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE content_id = $1`, episodeID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieID, seriesID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	// A movie whose only file in the folder is missing, and an episode with no
	// file at all: both memberships are stale.
	exec(`INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Deadlock Movie', 'matched', '{}'::text[]),
		       ($2, 'series', 'Deadlock Series', 'matched', '{}'::text[])`, movieID, seriesID)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, NOW())`, movieID, folderID)
	exec(`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, missing_since)
		VALUES ($1, $2, $3, 1024, NOW() - INTERVAL '3 days')`,
		movieID, folderID, fmt.Sprintf("/mdr-%d/Deadlock Movie (2020).mkv", suffix))
	exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Episode')`, episodeID, seriesID)
	exec(`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, NOW())`, episodeID, folderID)

	installDeadlockOnce := func(table string) (attempts func() int64) {
		t.Helper()
		name := fmt.Sprintf("mdr_%s_%d", table, suffix)
		exec(fmt.Sprintf(`CREATE SEQUENCE %s_attempts`, name))
		exec(fmt.Sprintf(`CREATE FUNCTION %[1]s_fn() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				-- Nested so other tests' deletes never advance the sequence.
				IF OLD.media_folder_id = %[2]d THEN
					IF nextval('%[1]s_attempts') = 1 THEN
						RAISE EXCEPTION 'synthetic deadlock' USING ERRCODE = '40P01';
					END IF;
				END IF;
				RETURN OLD;
			END $$`, name, folderID))
		exec(fmt.Sprintf(`CREATE TRIGGER %[1]s_trg BEFORE DELETE ON %[2]s FOR EACH ROW EXECUTE FUNCTION %[1]s_fn()`, name, table))
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s_trg ON %s`, name, table))
			_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s_fn()`, name))
			_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP SEQUENCE IF EXISTS %s_attempts`, name))
		})
		return func() int64 {
			t.Helper()
			var n int64
			if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT last_value FROM %s_attempts`, name)).Scan(&n); err != nil {
				t.Fatalf("read attempts for %s: %v", table, err)
			}
			return n
		}
	}

	episodeAttempts := installDeadlockOnce("episode_libraries")
	removedEpisodes, err := NewEpisodeLibraryRepository(pool).RemoveStaleFolderMemberships(ctx, folderID)
	if err != nil {
		t.Fatalf("RemoveStaleFolderMemberships after one deadlock: %v", err)
	}
	if removedEpisodes != 1 {
		t.Fatalf("removed episode memberships = %d, want 1", removedEpisodes)
	}
	if got := episodeAttempts(); got != 2 {
		t.Fatalf("episode reconciliation attempts = %d, want 2 (one deadlock, one retry)", got)
	}

	itemAttempts := installDeadlockOnce("media_item_libraries")
	removed, deleted, _, err := NewLibraryItemRepository(pool).ReconcileFolderMembership(ctx, folderID, nil)
	if err != nil {
		t.Fatalf("ReconcileFolderMembership after one deadlock: %v", err)
	}
	if removed != 1 || deleted != 1 {
		t.Fatalf("removed memberships = %d, deleted items = %d, want 1 and 1", removed, deleted)
	}
	if got := itemAttempts(); got != 2 {
		t.Fatalf("item reconciliation attempts = %d, want 2 (one deadlock, one retry)", got)
	}
}

// TestStaleMembershipRemovalLocksSeriesFirstDB covers the lock order between
// stale membership removal and curated rebind or split, which lock the series
// row and then delete its memberships without retrying a deadlock. Removal
// must wait on the series before it deletes any membership, so the curation
// transaction's delete never waits on it.
func TestStaleMembershipRemovalLocksSeriesFirstDB(t *testing.T) {
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
	seriesID := fmt.Sprintf("mlo-series-%d", suffix)
	episodeID := fmt.Sprintf("mlo-episode-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('tv', 'Lock Order Test', true) RETURNING id`,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', 'Lock Order Series', 'matched', '{}'::text[])`, []any{seriesID}},
		{`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Episode')`, []any{episodeID, seriesID}},
		{`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, NOW())`, []any{episodeID, folderID}},
	} {
		if _, err := pool.Exec(ctx, seed.query, seed.args...); err != nil {
			t.Fatalf("seed %q: %v", seed.query, err)
		}
	}

	// The curation transaction locks the series first.
	curation, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin curation: %v", err)
	}
	defer func() { _ = curation.Rollback(ctx) }()
	var curationPID int
	if err := curation.QueryRow(ctx,
		`SELECT pg_backend_pid() FROM media_items WHERE content_id = $1 FOR UPDATE`, seriesID,
	).Scan(&curationPID); err != nil {
		t.Fatalf("lock series: %v", err)
	}

	removed := make(chan error, 1)
	go func() {
		_, err := NewEpisodeLibraryRepository(pool).RemoveStaleFolderMemberships(ctx, folderID)
		removed <- err
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for blocked := false; !blocked; {
		if err := pool.QueryRow(waitCtx, `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND $1::int = ANY(pg_blocking_pids(pid)))
		`, curationPID).Scan(&blocked); err != nil {
			t.Fatalf("waiting for membership removal to block on the series: %v", err)
		}
		select {
		case err := <-removed:
			t.Fatalf("membership removal finished while the series was locked: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Removing memberships first would make this delete wait on it, then
	// deadlock once removal waits on the series.
	if _, err := curation.Exec(ctx, `SET LOCAL lock_timeout = '500ms'`); err != nil {
		t.Fatalf("set lock timeout: %v", err)
	}
	if _, err := curation.Exec(ctx, `DELETE FROM episode_libraries WHERE episode_id = $1`, episodeID); err != nil {
		t.Fatalf("curation delete waited on membership removal: %v", err)
	}
	if err := curation.Commit(ctx); err != nil {
		t.Fatalf("commit curation: %v", err)
	}
	if err := <-removed; err != nil {
		t.Fatalf("RemoveStaleFolderMemberships: %v", err)
	}
}

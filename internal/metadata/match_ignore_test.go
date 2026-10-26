package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestProcessLeavesIgnoredItemUnmatchedDB covers issue #2392: automatic
// matching skips an item an admin marked as intentionally unmatched in every
// mode a scan, the matcher queues or a refresh use, while the item is
// provisional.
func TestProcessLeavesIgnoredItemUnmatchedDB(t *testing.T) {
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

	contentID := fmt.Sprintf("local-match-ignore-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path, match_ignored_at)
		VALUES ($1, 'movie', 'Bonus Disc', 'unmatched', '{}'::text[], '', '', '', NOW())
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	svc := &MetadataService{dbPool: pool}
	expect := func(step string, want bool) {
		t.Helper()
		got, err := svc.matchIgnored(ctx, contentID)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if got != want {
			t.Fatalf("%s: ignored = %v, want %v", step, got, want)
		}
	}

	expect("ignored unmatched item", true)
	for _, mode := range []RefreshMode{ModeInitialMatch, ModeScheduledRefresh, ModeManualRefresh} {
		result, err := svc.Process(ctx, ProcessRequest{ContentID: contentID, Mode: mode})
		if err != nil {
			t.Fatalf("Process mode %d: %v", mode, err)
		}
		if result == nil || !result.Pinned || result.Updated || result.ContentID != contentID {
			t.Fatalf("Process mode %d result = %+v, want pinned and not updated", mode, result)
		}
	}

	if _, err := pool.Exec(ctx, `UPDATE media_items SET status = 'matched' WHERE content_id = $1`, contentID); err != nil {
		t.Fatalf("match item: %v", err)
	}
	expect("a matched item", false)
	if _, err := pool.Exec(ctx, `UPDATE media_items SET status = 'unmatched', match_ignored_at = NULL WHERE content_id = $1`, contentID); err != nil {
		t.Fatalf("clear mark: %v", err)
	}
	expect("an unmatched item without the mark", false)
}

// TestMatchQueuesSkipIgnoredItemsDB covers the matcher queues for issue
// #2392: neither queues the files of an ignored movie or series, a sync
// drops rows they already had, and clearing the mark queues them again.
func TestMatchQueuesSkipIgnoredItemsDB(t *testing.T) {
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
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	seed := func(folderType, itemType string) (folderID int, contentID, root string, fileIDs []int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`,
			folderType, fmt.Sprintf("Match ignore %s %d", folderType, suffix)).Scan(&folderID); err != nil {
			t.Fatalf("seed folder: %v", err)
		}
		contentID = fmt.Sprintf("local-match-ignore-%s-%d", itemType, suffix)
		root = fmt.Sprintf("/match-ignore-%d/%s/Title (2003)", suffix, itemType)
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
			_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
			_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
		})
		exec(`INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path, match_ignored_at)
			VALUES ($1, $2, 'Title', 'unmatched', '{}'::text[], '', '', '', NOW())`, contentID, itemType)
		names := []string{"Title (2003).mkv"}
		if itemType == "series" {
			names = []string{"Season 01/S01E01.mkv", "Season 01/S01E02.mkv"}
		}
		for _, name := range names {
			var id int
			if err := pool.QueryRow(ctx, `
				INSERT INTO media_files (content_id, media_folder_id, file_path, observed_root_path, file_size, base_type)
				VALUES ($1, $2, $3, $4, 1024, $5) RETURNING id
			`, contentID, folderID, root+"/"+name, root, itemType).Scan(&id); err != nil {
				t.Fatalf("seed file: %v", err)
			}
			fileIDs = append(fileIDs, id)
		}
		return folderID, contentID, root, fileIDs
	}

	movieFolder, movieID, _, movieFiles := seed("movies", "movie")
	seriesFolder, seriesID, seriesRoot, _ := seed("series", "series")
	movies := NewMovieMatchQueueRepository(pool, nil)
	series := NewSeriesRootMatchQueueRepository(pool)
	enqueue := func(step string) {
		t.Helper()
		if err := movies.EnqueueMovieFile(ctx, movieFiles[0]); err != nil {
			t.Fatalf("%s: enqueue movie: %v", step, err)
		}
		if err := series.EnqueueSeriesRoot(ctx, seriesFolder, seriesRoot); err != nil {
			t.Fatalf("%s: enqueue series root: %v", step, err)
		}
	}
	sync := func(step string) {
		t.Helper()
		if err := movies.SyncForFolder(ctx, movieFolder); err != nil {
			t.Fatalf("%s: sync movies: %v", step, err)
		}
		if err := series.SyncForFolder(ctx, seriesFolder); err != nil {
			t.Fatalf("%s: sync series: %v", step, err)
		}
	}
	queued := func() (int, int) {
		t.Helper()
		return count(`SELECT COUNT(*) FROM movie_match_queue WHERE media_folder_id = $1`, movieFolder),
			count(`SELECT COUNT(*) FROM series_root_match_queue WHERE media_folder_id = $1`, seriesFolder)
	}
	setIgnored := func(ignored bool) {
		t.Helper()
		exec(`UPDATE media_items SET match_ignored_at = CASE WHEN $2 THEN NOW() END WHERE content_id = ANY($1)`, []string{movieID, seriesID}, ignored)
	}

	enqueue("ignored")
	sync("ignored")
	if m, s := queued(); m != 0 || s != 0 {
		t.Fatalf("ignored items queued: movie rows = %d, series rows = %d, want none", m, s)
	}

	setIgnored(false)
	enqueue("not ignored")
	if m, s := queued(); m != 1 || s != 1 {
		t.Fatalf("not ignored: movie rows = %d, series rows = %d, want one each", m, s)
	}

	// Rows queued before the mark, even parked ones asked to rerun, drain on
	// the next sync.
	exec(`UPDATE movie_match_queue SET state = 'parked', parked_at = NOW(), rerun_requested = true WHERE media_folder_id = $1`, movieFolder)
	exec(`UPDATE series_root_match_queue SET state = 'parked', parked_at = NOW(), rerun_requested = true WHERE media_folder_id = $1`, seriesFolder)
	setIgnored(true)
	sync("ignored again")
	if m, s := queued(); m != 0 || s != 0 {
		t.Fatalf("after ignoring queued items: movie rows = %d, series rows = %d, want none", m, s)
	}
}

// TestRefreshLeavesIgnoredItemAloneDB covers the refresh paths for issue
// #2392: a refresh of an ignored item is a completed no-op rather than a
// failure, and a season or episode refresh of an ignored series stops
// before it searches a provider.
func TestRefreshLeavesIgnoredItemAloneDB(t *testing.T) {
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
	movieID := fmt.Sprintf("local-refresh-ignore-movie-%d", suffix)
	seriesID := fmt.Sprintf("local-refresh-ignore-series-%d", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM metadata_refresh_debt WHERE content_id = ANY($1)`, []string{movieID, seriesID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieID, seriesID})
	})
	for _, row := range []struct{ id, typ string }{{movieID, "movie"}, {seriesID, "series"}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path, match_ignored_at)
			VALUES ($1, $2, 'Bonus Disc', 'unmatched', '{}'::text[], '', '', '', NOW())
		`, row.id, row.typ); err != nil {
			t.Fatalf("seed item: %v", err)
		}
	}
	// A scheduled refresh claimed the movie's debt row: the pinned refresh
	// must settle it, or the row is claimed again once its lease lapses.
	if _, err := pool.Exec(ctx, `
		INSERT INTO metadata_refresh_debt (target_type, content_id, next_refresh_at, claimed_at, lease_expires_at, attempt_count)
		VALUES ('item', $1, NOW() - interval '1 minute', NOW(), NOW() + interval '15 minutes', 1)
	`, movieID); err != nil {
		t.Fatalf("seed refresh debt: %v", err)
	}
	svc := &MetadataService{
		dbPool:          pool,
		itemRepo:        catalog.NewItemRepository(pool),
		seasonRepo:      catalog.NewSeasonRepository(pool),
		episodeRepo:     catalog.NewEpisodeRepository(pool),
		refreshDebtRepo: NewRefreshDebtRepository(pool),
	}

	if err := svc.refreshItemTarget(ctx, movieID, 0, ModeManualRefresh, false); err != nil {
		t.Fatalf("refresh of an ignored movie = %v, want no error", err)
	}
	var failures int
	if err := pool.QueryRow(ctx, `SELECT refresh_failures FROM media_items WHERE content_id = $1`, movieID).Scan(&failures); err != nil {
		t.Fatalf("read refresh failures: %v", err)
	}
	if failures != 0 {
		t.Fatalf("refresh of an ignored movie recorded %d failures, want 0", failures)
	}
	var debtRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM metadata_refresh_debt WHERE content_id = $1`, movieID).Scan(&debtRows); err != nil {
		t.Fatalf("read refresh debt: %v", err)
	}
	if debtRows != 0 {
		t.Fatalf("refresh of an ignored movie left %d refresh debt rows, want the claimed row settled", debtRows)
	}

	if err := svc.refreshSeriesChildTarget(ctx, seriesID, 1, 1, 0, ModeManualRefresh); !errors.Is(err, ErrMetadataNotFound) {
		t.Fatalf("episode refresh of an ignored series = %v, want ErrMetadataNotFound", err)
	}
}

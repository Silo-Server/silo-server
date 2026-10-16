package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// TestIgnoredUnmatchedItemStaysIgnoredAcrossRescanDB covers issue #2392: an
// item an admin marks as intentionally unmatched leaves the unmatched list,
// the scan's retry list and the movie match queue (even a parked row), stays
// out after a rescan writes its file and item again and syncs the queue, and
// comes back, queued for matching, once the mark is cleared. A match clears
// the mark.
func TestIgnoredUnmatchedItemStaysIgnoredAcrossRescanDB(t *testing.T) {
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
	// A per-run tag in the title scopes every list search to this test's rows.
	tag := fmt.Sprintf("ignore2392-%d", suffix)
	contentID := fmt.Sprintf("local-ignore-%d", suffix)
	root := fmt.Sprintf("/ignore-%d/Bonus Disc (2003)", suffix)
	filePath := root + "/Bonus Disc (2003).mkv"
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, tag).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_item_libraries WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	items := catalog.NewItemRepository(pool)
	files := scanner.NewFileRepository(pool)
	movieQueue := metadata.NewMovieMatchQueueRepository(pool, files)
	// scan writes the item and its file the way a scan of the unchanged file
	// does, so a rescan can be repeated.
	var fileID int
	scan := func(step string) {
		t.Helper()
		if err := items.Upsert(ctx, &models.MediaItem{ContentID: contentID, Type: "movie", Title: tag + " Bonus Disc", Year: 2003, Status: "unmatched"}); err != nil {
			t.Fatalf("%s: upsert item: %v", step, err)
		}
		modifiedAt := time.Unix(1_700_000_000, 0).UTC()
		file, err := files.Upsert(ctx, models.MediaFile{ContentID: contentID, MediaFolderID: folderID, FilePath: filePath, ObservedRootPath: root, FileSize: 1024, FileModifiedAt: &modifiedAt})
		if err != nil {
			t.Fatalf("%s: upsert file: %v", step, err)
		}
		fileID = file.ID
		if err := movieQueue.SyncForFolder(ctx, folderID); err != nil {
			t.Fatalf("%s: sync movie queue: %v", step, err)
		}
	}
	scan("first scan")
	// The matcher gave up on the file: its queue row is parked, which the
	// Metadata Matcher section counts as still needing a match.
	if _, err := pool.Exec(ctx, `UPDATE movie_match_queue SET state = 'parked', parked_at = NOW(), attempt_count = 3 WHERE media_file_id = $1`, fileID); err != nil {
		t.Fatalf("park queue row: %v", err)
	}
	queueState := func(step string) string {
		t.Helper()
		var state string
		err := pool.QueryRow(ctx, `SELECT state FROM movie_match_queue WHERE media_file_id = $1`, fileID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return "none"
		}
		if err != nil {
			t.Fatalf("%s: read queue row: %v", step, err)
		}
		return state
	}
	if got := queueState("before ignoring"); got != "parked" {
		t.Fatalf("before ignoring: queue row = %s, want parked", got)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, contentID, folderID); err != nil {
		t.Fatalf("seed library membership: %v", err)
	}

	h := NewLibraryHandler(nil, nil, nil, pool, nil)
	h.MovieMatchQueueRepo = movieQueue
	h.SeriesMatchQueueRepo = metadata.NewSeriesRootMatchQueueRepository(pool)
	listed := func(step string, ignored bool) *UnmatchedItemView {
		t.Helper()
		views, total, err := h.ListUnmatchedItems(ctx, tag, ignored, 50, 0)
		if err != nil {
			t.Fatalf("%s: list (ignored=%v): %v", step, ignored, err)
		}
		if total != len(views) || total > 1 {
			t.Fatalf("%s: list (ignored=%v) total = %d with %d rows, want at most this test's item", step, ignored, total, len(views))
		}
		if total == 0 {
			return nil
		}
		return &views[0]
	}
	retried := func(step string) bool {
		t.Helper()
		ids, err := items.ListUnmatchedByFolderAndPathPrefix(ctx, folderID, root, 0)
		if err != nil {
			t.Fatalf("%s: retry list: %v", step, err)
		}
		for _, id := range ids {
			if id == contentID {
				return true
			}
		}
		return false
	}

	if v := listed("before ignoring", false); v == nil || v.ContentID != contentID || v.IgnoredAt != nil || v.LibraryName != tag {
		t.Fatalf("before ignoring: unmatched list = %+v, want the item without ignored_at", v)
	}
	if !retried("before ignoring") {
		t.Fatal("before ignoring: the scan's retry list leaves the item out")
	}

	if err := h.SetUnmatchedItemIgnored(ctx, contentID, true); err != nil {
		t.Fatalf("ignore: %v", err)
	}
	var ignoredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT match_ignored_at FROM media_items WHERE content_id = $1`, contentID).Scan(&ignoredAt); err != nil {
		t.Fatalf("read mark: %v", err)
	}
	if err := h.SetUnmatchedItemIgnored(ctx, contentID, true); err != nil {
		t.Fatalf("ignore again: %v", err)
	}

	for _, step := range []string{"after ignoring", "after a rescan"} {
		if step == "after a rescan" {
			scan(step)
		}
		if v := listed(step, false); v != nil {
			t.Fatalf("%s: unmatched list still holds %+v", step, v)
		}
		v := listed(step, true)
		if v == nil || v.ContentID != contentID || v.IgnoredAt == nil || !v.IgnoredAt.Equal(ignoredAt) {
			t.Fatalf("%s: ignored list = %+v, want the item ignored at %v", step, v, ignoredAt)
		}
		if retried(step) {
			t.Fatalf("%s: the scan's retry list still holds the item", step)
		}
		if got := queueState(step); got != "none" {
			t.Fatalf("%s: movie queue row = %s, want none", step, got)
		}
	}

	if err := h.SetUnmatchedItemIgnored(ctx, contentID, false); err != nil {
		t.Fatalf("unignore: %v", err)
	}
	if err := h.SetUnmatchedItemIgnored(ctx, contentID, false); err != nil {
		t.Fatalf("unignore again: %v", err)
	}
	if v := listed("after unignoring", false); v == nil || v.ContentID != contentID || v.IgnoredAt != nil {
		t.Fatalf("after unignoring: unmatched list = %+v, want the item back", v)
	}
	if v := listed("after unignoring", true); v != nil {
		t.Fatalf("after unignoring: ignored list still holds %+v", v)
	}
	if !retried("after unignoring") {
		t.Fatal("after unignoring: the scan's retry list leaves the item out")
	}
	if got := queueState("after unignoring"); got != "pending" {
		t.Fatalf("after unignoring: movie queue row = %s, want pending", got)
	}

	// A match clears the mark, and a matched item cannot be ignored.
	if err := h.SetUnmatchedItemIgnored(ctx, contentID, true); err != nil {
		t.Fatalf("ignore before matching: %v", err)
	}
	if err := items.Upsert(ctx, &models.MediaItem{ContentID: contentID, Type: "movie", Title: tag + " Bonus Disc", Year: 2003, Status: "matched"}); err != nil {
		t.Fatalf("match: %v", err)
	}
	var cleared bool
	if err := pool.QueryRow(ctx, `SELECT match_ignored_at IS NULL FROM media_items WHERE content_id = $1`, contentID).Scan(&cleared); err != nil {
		t.Fatalf("read mark after match: %v", err)
	}
	if !cleared {
		t.Fatal("a match left match_ignored_at set")
	}
	requireAPIStatus(t, h.SetUnmatchedItemIgnored(ctx, contentID, true), http.StatusConflict)
	// Books keep their own enrichment paths, so a pending book cannot be ignored.
	bookID := contentID + "-book"
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, bookID) })
	if err := items.Upsert(ctx, &models.MediaItem{ContentID: bookID, Type: "audiobook", Title: tag + " Book", Status: "pending"}); err != nil {
		t.Fatalf("seed book: %v", err)
	}
	requireAPIStatus(t, h.SetUnmatchedItemIgnored(ctx, bookID, true), http.StatusConflict)
	requireAPIStatus(t, h.SetUnmatchedItemIgnored(ctx, contentID+"-missing", true), http.StatusNotFound)
	requireAPIStatus(t, h.SetUnmatchedItemIgnored(ctx, contentID+"-missing", false), http.StatusNotFound)

	// A series goes through the series root queue the same way.
	var seriesFolder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, tag+" shows").Scan(&seriesFolder); err != nil {
		t.Fatalf("seed series folder: %v", err)
	}
	seriesID := contentID + "-series"
	seriesRoot := fmt.Sprintf("/ignore-%d/Backyard Workshop Diaries", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, seriesFolder)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, seriesFolder)
	})
	if err := items.Upsert(ctx, &models.MediaItem{ContentID: seriesID, Type: "series", Title: tag + " Series", Status: "unmatched"}); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	for _, name := range []string{"S01E01.mkv", "S01E02.mkv"} {
		if _, err := files.Upsert(ctx, models.MediaFile{ContentID: seriesID, MediaFolderID: seriesFolder, FilePath: seriesRoot + "/Season 01/" + name, ObservedRootPath: seriesRoot, FileSize: 1024, BaseType: "series"}); err != nil {
			t.Fatalf("seed episode file: %v", err)
		}
	}
	seriesQueued := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM series_root_match_queue WHERE media_folder_id = $1`, seriesFolder).Scan(&n); err != nil {
			t.Fatalf("count series queue rows: %v", err)
		}
		return n
	}
	if err := h.SeriesMatchQueueRepo.EnqueueSeriesRoot(ctx, seriesFolder, seriesRoot); err != nil {
		t.Fatalf("queue series root: %v", err)
	}
	if got := seriesQueued(); got != 1 {
		t.Fatalf("before ignoring the series: series queue rows = %d, want 1", got)
	}
	if err := h.SetUnmatchedItemIgnored(ctx, seriesID, true); err != nil {
		t.Fatalf("ignore series: %v", err)
	}
	if got := seriesQueued(); got != 0 {
		t.Fatalf("after ignoring the series: series queue rows = %d, want 0", got)
	}
	if err := h.SetUnmatchedItemIgnored(ctx, seriesID, false); err != nil {
		t.Fatalf("unignore series: %v", err)
	}
	if got := seriesQueued(); got != 1 {
		t.Fatalf("after unignoring the series: series queue rows = %d, want 1", got)
	}

	// A partial split leaves a second provisional item under the same root.
	// Ignoring one keeps the root's row for the other, and a restore must wake
	// that row even when the matcher had parked it.
	splitID := seriesID + "-split"
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, splitID) })
	if err := items.Upsert(ctx, &models.MediaItem{ContentID: splitID, Type: "series", Title: tag + " Series Split", Status: "unmatched"}); err != nil {
		t.Fatalf("seed split series: %v", err)
	}
	if _, err := files.Upsert(ctx, models.MediaFile{ContentID: splitID, MediaFolderID: seriesFolder, FilePath: seriesRoot + "/Season 02/S02E01.mkv", ObservedRootPath: seriesRoot, FileSize: 1024, BaseType: "series"}); err != nil {
		t.Fatalf("seed split episode file: %v", err)
	}
	if err := h.SeriesMatchQueueRepo.EnqueueSeriesRoot(ctx, seriesFolder, seriesRoot); err != nil {
		t.Fatalf("queue shared series root: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE series_root_match_queue SET state = 'parked', parked_at = NOW(), deterministic_attempt_count = 3 WHERE media_folder_id = $1`, seriesFolder); err != nil {
		t.Fatalf("park shared series root: %v", err)
	}
	seriesState := func(step string) string {
		t.Helper()
		var state string
		if err := pool.QueryRow(ctx, `SELECT state FROM series_root_match_queue WHERE media_folder_id = $1`, seriesFolder).Scan(&state); err != nil {
			t.Fatalf("%s: read series queue row: %v", step, err)
		}
		return state
	}
	if err := h.SetUnmatchedItemIgnored(ctx, seriesID, true); err != nil {
		t.Fatalf("ignore series sharing a root: %v", err)
	}
	if got := seriesState("after ignoring one item of a shared root"); got != "parked" {
		t.Fatalf("after ignoring one item of a shared root: series queue row = %s, want parked", got)
	}
	if err := h.SetUnmatchedItemIgnored(ctx, seriesID, false); err != nil {
		t.Fatalf("unignore series sharing a root: %v", err)
	}
	if got := seriesState("after restoring one item of a shared root"); got != "pending" {
		t.Fatalf("after restoring one item of a shared root: series queue row = %s, want pending", got)
	}
}

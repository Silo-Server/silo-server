package adminjob

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLibraryRefreshSkipsIgnoredItemsDB covers issue #2392: a library
// refresh leaves out an item an admin marked as intentionally unmatched, in
// both modes, while it still lists the library's other unmatched items. An
// ignored audiobook is listed for a tags-only re-read; the executor drops it
// from a quick refresh.
func TestLibraryRefreshSkipsIgnoredItemsDB(t *testing.T) {
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
	ignoredID := fmt.Sprintf("local-refresh-ignored-%d", suffix)
	unmatchedID := fmt.Sprintf("local-refresh-unmatched-%d", suffix)
	audiobookID := fmt.Sprintf("local-refresh-ignored-audiobook-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("Refresh ignore %d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{ignoredID, unmatchedID, audiobookID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	for _, row := range []struct {
		id      string
		ignored bool
	}{{ignoredID, true}, {unmatchedID, false}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, tmdb_id, match_ignored_at)
			VALUES ($1, 'movie', 'Bonus Disc', 'unmatched', '{}'::text[], '42', CASE WHEN $2 THEN NOW() END)
		`, row.id, row.ignored); err != nil {
			t.Fatalf("seed item %s: %v", row.id, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, row.id, folderID); err != nil {
			t.Fatalf("link item %s: %v", row.id, err)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, match_ignored_at)
		VALUES ($1, 'audiobook', 'Bonus Disc', '', '{}'::text[], NOW())
	`, audiobookID); err != nil {
		t.Fatalf("seed audiobook: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, audiobookID, folderID); err != nil {
		t.Fatalf("link audiobook: %v", err)
	}

	lister := NewPGLibraryRefreshItemLister(pool)
	for _, tc := range []struct {
		mode LibraryRefreshMode
		want []string
	}{
		{LibraryRefreshModeQuick, []string{unmatchedID}},
		{LibraryRefreshModeFull, []string{audiobookID + " audiobook", unmatchedID}},
	} {
		items, err := lister.ListLibraryItems(ctx, folderID, tc.mode)
		if err != nil {
			t.Fatalf("ListLibraryItems(%s): %v", tc.mode, err)
		}
		var got []string
		for _, item := range items {
			if item.Audiobook {
				got = append(got, item.ContentID+" audiobook")
			} else {
				got = append(got, item.ContentID)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("ListLibraryItems(%s) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

package adminjob

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestQuickRefreshListsLanguageMismatchedItems covers the listing half of
// issue #211: quick-mode library refresh must include items whose stamped
// default_metadata_language differs from the library's configured metadata
// language, even when the item is otherwise complete (overview, poster,
// backdrop all present) — otherwise a library language change never revisits
// already-complete items.
func TestQuickRefreshListsLanguageMismatchedItems(t *testing.T) {
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
	mismatchID := fmt.Sprintf("lang-mismatch-%d", suffix)
	matchedID := fmt.Sprintf("lang-matched-%d", suffix)

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled, metadata_language)
		VALUES ('movies', 'Lang Refresh Test', true, 'da')
		RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{mismatchID, matchedID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	for _, row := range []struct {
		id, lang string
	}{
		{mismatchID, "zh"},
		{matchedID, "da"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (
				content_id, type, title, status, genres, tmdb_id,
				default_metadata_language, overview, poster_path, backdrop_path,
				last_refreshed, refresh_failures, episode_metadata_incomplete
			) VALUES ($1, 'movie', 'Complete Item', 'matched', '{}'::text[], '42',
				$2, 'An overview', '/p.jpg', '/b.jpg', NOW(), 0, FALSE)
		`, row.id, row.lang); err != nil {
			t.Fatalf("seed media item %s: %v", row.id, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_item_libraries (content_id, media_folder_id)
			VALUES ($1, $2)
		`, row.id, folderID); err != nil {
			t.Fatalf("link media item %s: %v", row.id, err)
		}
	}

	lister := NewPGLibraryRefreshItemLister(pool)
	items, err := lister.ListLibraryItems(ctx, folderID, LibraryRefreshModeQuick)
	if err != nil {
		t.Fatalf("ListLibraryItems: %v", err)
	}

	var sawMismatch, sawMatched bool
	for _, item := range items {
		switch item.ContentID {
		case mismatchID:
			sawMismatch = true
		case matchedID:
			sawMatched = true
		}
	}
	if !sawMismatch {
		t.Errorf("quick refresh must include complete item with stamped language differing from the library language")
	}
	if sawMatched {
		t.Errorf("quick refresh must not include complete item whose stamped language matches the library language")
	}
}

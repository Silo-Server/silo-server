package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestReconcileRelinkedItemsKeepsItemsThatStillHaveFiles covers cleanup after
// a relink outside the scanner: the replaced item's stale membership goes, and
// the item is deleted only when no file row links to it at all.
func TestReconcileRelinkedItemsKeepsItemsThatStillHaveFiles(t *testing.T) {
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
	id := func(name string) string { return fmt.Sprintf("relink-%s-%d", name, suffix) }
	fileless, missingOnly, otherFolder, stillPresent, untouched := id("fileless"), id("missing"), id("other"), id("present"), id("untouched")
	// An earlier relink already removed this item's membership and kept it
	// for its remaining file; that file has now been relinked away too.
	unlisted := id("unlisted")
	all := []string{fileless, missingOnly, otherFolder, stillPresent, untouched, unlisted}

	folders := make([]int, 2)
	for i := range folders {
		if err := pool.QueryRow(ctx,
			`INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`,
			fmt.Sprintf("Relink %d-%d", suffix, i),
		).Scan(&folders[i]); err != nil {
			t.Fatalf("seed folder: %v", err)
		}
	}
	folder, other := folders[0], folders[1]
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, all)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, folders)
	})

	for _, contentID := range all {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path)
			VALUES ($1, 'series', $1, 'unmatched', '{}'::text[], '', '', '')
		`, contentID); err != nil {
			t.Fatalf("seed item: %v", err)
		}
		if contentID == unlisted {
			continue
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, folder,
		); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, otherFolder, other,
	); err != nil {
		t.Fatalf("seed other-folder membership: %v", err)
	}
	seedFile := func(contentID string, folderID int, missing bool) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, missing_since)
			VALUES ($1, $2, $3, 1024, CASE WHEN $4 THEN NOW() ELSE NULL END)
		`, contentID, folderID, fmt.Sprintf("/relink-%d/%s-%d.mkv", suffix, contentID, folderID), missing); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}
	seedFile(missingOnly, folder, true)
	seedFile(otherFolder, other, false)
	seedFile(stillPresent, folder, false)
	seedFile(untouched, folder, false)

	repo := NewLibraryItemRepository(pool)
	removed, deleted, _, err := repo.ReconcileRelinkedItems(ctx, folder, []string{fileless, missingOnly, otherFolder, stillPresent, unlisted})
	if err != nil {
		t.Fatalf("ReconcileRelinkedItems: %v", err)
	}
	if removed != 3 || deleted != 2 {
		t.Fatalf("removed/deleted = %d/%d, want 3/2", removed, deleted)
	}

	state := func(contentID string) (item, membership bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM media_items WHERE content_id = $1),
			       EXISTS(SELECT 1 FROM media_item_libraries WHERE content_id = $1 AND media_folder_id = $2)
		`, contentID, folder).Scan(&item, &membership); err != nil {
			t.Fatalf("read state: %v", err)
		}
		return item, membership
	}
	for _, tc := range []struct {
		contentID              string
		wantItem, wantInFolder bool
	}{
		{contentID: fileless, wantItem: false, wantInFolder: false},
		{contentID: unlisted, wantItem: false, wantInFolder: false},
		// A missing file may sit under an unreachable root; a scan decides.
		{contentID: missingOnly, wantItem: true, wantInFolder: false},
		{contentID: otherFolder, wantItem: true, wantInFolder: false},
		{contentID: stillPresent, wantItem: true, wantInFolder: true},
		{contentID: untouched, wantItem: true, wantInFolder: true},
	} {
		if item, inFolder := state(tc.contentID); item != tc.wantItem || inFolder != tc.wantInFolder {
			t.Errorf("%s: item=%v membership=%v, want %v/%v", tc.contentID, item, inFolder, tc.wantItem, tc.wantInFolder)
		}
	}

	if removed, deleted, _, err := repo.ReconcileRelinkedItems(ctx, folder, nil); err != nil || removed != 0 || deleted != 0 {
		t.Fatalf("empty list removed/deleted = %d/%d, err = %v, want 0/0", removed, deleted, err)
	}
}

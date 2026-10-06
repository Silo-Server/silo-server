package scanner

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// TestUpdateContentIDByObservedRootPathReportsReplacedItems checks what the
// series-root relink hands to membership cleanup, and that a file an admin
// split pinned to another item stays where the split put it.
func TestUpdateContentIDByObservedRootPathReportsReplacedItems(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "series", "Root relink")
	suffix := time.Now().UnixNano()
	root := fmt.Sprintf("/root-relink-%d/Show", suffix)
	id := func(name string) string { return fmt.Sprintf("relink-%s-%d", name, suffix) }
	target, old, other, pinned := id("target"), id("old"), id("other"), id("pinned")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_identity_overrides WHERE media_folder_id = $1`, folderID)
	})
	seed := func(name, contentID string, missing bool) string {
		t.Helper()
		path := root + "/" + name
		var link any
		if contentID != "" {
			link = contentID
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, observed_root_path, content_id, file_size, missing_since)
			VALUES ($1, $2, $3, $4, 1024, CASE WHEN $5 THEN NOW() ELSE NULL END)
		`, folderID, path, root, link, missing); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return path
	}
	seed("e01.mkv", old, false)
	seed("e02.mkv", old, false)
	seed("e03.mkv", "", false)
	seed("e04.mkv", target, false)
	seed("e05.mkv", other, true)
	pinnedPath := seed("e06.mkv", pinned, false)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_identity_overrides (media_folder_id, scope, file_path, forced_type, forced_title)
		VALUES ($1, 'file', $2, 'series', 'Pinned')
	`, folderID, pinnedPath); err != nil {
		t.Fatalf("seed file override: %v", err)
	}

	updated, replaced, err := NewFileRepository(pool).UpdateContentIDByObservedRootPath(ctx, folderID, root, target)
	if err != nil {
		t.Fatalf("UpdateContentIDByObservedRootPath: %v", err)
	}
	// e01-e03 move; e04 already links to the target, e05 is missing, and e06
	// is pinned by the split's override.
	if updated != 3 || !slices.Equal(replaced, []string{old}) {
		t.Fatalf("updated=%d replaced=%q, want 3 [%s]", updated, replaced, old)
	}
	var pinnedLink string
	if err := pool.QueryRow(ctx, `SELECT content_id FROM media_files WHERE file_path = $1`, pinnedPath).Scan(&pinnedLink); err != nil {
		t.Fatalf("read pinned file: %v", err)
	}
	if pinnedLink != pinned {
		t.Fatalf("pinned file relinked to %s, want %s", pinnedLink, pinned)
	}
}

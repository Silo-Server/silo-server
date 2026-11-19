package scanner

import (
	"context"
	"testing"
)

// TestScannedGroupForItemDB checks that Match Item reads the scanner's own
// identity for an item's folder: the group of the item's files in that
// library, the one holding most of them when they span several, and nothing
// from other items or libraries. It goes through the files rather than group
// claims, which the scanner does not record for every item.
func TestScannedGroupForItemDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Scanned group lookup")
	otherFolderID := seedDeadRootTestFolder(t, pool, "movies", "Scanned group lookup other")

	const itemID, otherItemID = "scanned-group-lookup-item", "scanned-group-lookup-other"
	for _, id := range []string{itemID, otherItemID} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, year) VALUES ($1, 'movie', 'Parasite', 1982)`, id); err != nil {
			t.Fatalf("seed item: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE content_id IN ($1, $2)`, itemID, otherItemID)
		_, _ = pool.Exec(ctx, `DELETE FROM scanned_media_groups WHERE media_folder_id IN ($1, $2)`, folderID, otherFolderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id IN ($1, $2)`, itemID, otherItemID)
	})

	seed := func(folder int, key, contentID, title string, year, files int, tmdb, imdb string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO scanned_media_groups (media_folder_id, group_key_version, content_group_key, base_title, base_year, tmdb_id, imdb_id)
			VALUES ($1, 1, $2, $3, $4, $5, $6)`, folder, key, title, year, tmdb, imdb); err != nil {
			t.Fatalf("seed group %s: %v", key, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, group_key_version, content_group_key, file_path, file_size)
			SELECT $1, $2, 1, $3, '/media/' || $3 || '/' || n || '.mkv', 1024
			FROM generate_series(1, $4::int) n`, contentID, folder, key, files); err != nil {
			t.Fatalf("seed files %s: %v", key, err)
		}
	}
	seed(folderID, "small", itemID, "Parasite Extras", 2019, 1, "", "")
	seed(folderID, "main", itemID, "Parasite", 2019, 3, "496243", "tt6751668")
	seed(folderID, "other-item", otherItemID, "Other", 2001, 9, "1", "")
	seed(otherFolderID, "other-folder", itemID, "Elsewhere", 2005, 9, "2", "")

	repo := NewScannedGroupRepository(pool)
	group, err := repo.GetForItem(ctx, folderID, itemID)
	if err != nil {
		t.Fatalf("GetForItem: %v", err)
	}
	if group == nil || group.BaseTitle != "Parasite" || group.BaseYear != 2019 || group.TmdbID != "496243" || group.ImdbID != "tt6751668" {
		t.Fatalf("GetForItem = %+v, want the item's largest group in this library", group)
	}

	none, err := repo.GetForItem(ctx, folderID, "scanned-group-lookup-missing")
	if err != nil || none != nil {
		t.Fatalf("GetForItem for an item without groups = %+v, %v; want nil, nil", none, err)
	}
}

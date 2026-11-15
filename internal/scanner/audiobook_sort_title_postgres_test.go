package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A rescan fills an empty sort title from the title only while the Name lock
// is off. With it on, a sort title the admin cleared stays empty. A sort title
// derived from an earlier title is derived again from the current one.
func TestAudiobookRescanSortTitleFollowsNameLockDB(t *testing.T) {
	for _, tc := range []struct {
		name   string
		locked []int
		stored string
		want   string
	}{
		{"name locked keeps a cleared sort title", []int{lockedFieldName}, "", ""},
		{"unlocked derives the sort title", []int{}, "", "Hobbit, The"},
		{"unlocked replaces a sort title left from an earlier title", []int{}, "Saga Name 1 - The Hobbit, The", "Hobbit, The"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newBookScanTestPool(t)
			folder := newBookScanTestFolder(t, pool, "audiobooks")
			ctx := t.Context()
			dir := t.TempDir()
			path := filepath.Join(dir, "book.m4b")
			if err := os.WriteFile(path, []byte("book"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf("sort-title-lock-%d", folder.ID)
			// The admin cleared Sort Title, which the edit dialog stores under the Name lock.
			if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,sort_title,status,genres,locked_fields)
VALUES ($1,'audiobook','The Hobbit',$3,'local','{}'::text[],$2)`, id, tc.locked, tc.stored); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, id, folder.ID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id=$1`, id) })

			s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
			book := &parsedAudiobook{Title: "The Hobbit", Files: []parsedAudiobookFile{{
				Path: path, Duration: 36000, Size: info.Size(), ModifiedAt: normalizeFileModifiedAt(info.ModTime()),
			}}}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if err := s.upsertAudiobookPresentationTx(ctx, tx, folder, id, dir, canonicalTestPath(t, dir), book, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			var got string
			if err := pool.QueryRow(ctx, `SELECT sort_title FROM media_items WHERE content_id=$1`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("sort_title = %q, want %q", got, tc.want)
			}
		})
	}
}

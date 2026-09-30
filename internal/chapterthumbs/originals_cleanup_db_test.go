package chapterthumbs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// The reference check and repoint run against real chapter rows: a row an
// earlier build saved back onto an original is moved to the w300 image beside
// it before that original is deleted, a chapter with no w300 image keeps its
// original, and w300 objects are never candidates.
func TestOriginalsCleanerReferenceCheckPostgres(t *testing.T) {
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
	var folderID, fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`,
		fmt.Sprintf("chapter-originals-test-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID) })
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, chapters)
		VALUES ($1, $2, '[]'::jsonb) RETURNING id`,
		folderID, fmt.Sprintf("/chapter-originals-test/%d.mkv", suffix)).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID) })

	// Chapter 0 was migrated to w300. Chapters 1 and 2 were saved by an
	// earlier build mid-upgrade and point at their originals; chapter 2 has no
	// w300 image.
	key := func(chapter int, name string) string {
		return fmt.Sprintf("chapter-images/%d/%d/%s", fileID, chapter, name)
	}
	chapters := fmt.Sprintf(`[{"index": 0, "thumbnail_path": %q}, {"index": 1, "thumbnail_path": %q, "thumbnail_thumbhash": "h1"}, {"index": 2, "thumbnail_path": %q}]`,
		key(0, "w300.webp"), key(1, "original.webp"), key(2, "original.webp"))
	if _, err := pool.Exec(ctx, `UPDATE media_files SET chapters = $2::jsonb WHERE id = $1`, fileID, chapters); err != nil {
		t.Fatalf("seed chapters: %v", err)
	}

	root := t.TempDir()
	store, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, k := range []string{key(0, "original.webp"), key(0, "w300.webp"), key(1, "original.webp"), key(1, "w300.webp"), key(2, "original.webp")} {
		if err := store.Put(ctx, k, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(k)), old, old); err != nil {
			t.Fatal(err)
		}
	}

	cleaner := NewOriginalsCleaner(pool, store)
	var stats OriginalsCleanupStats
	acquired, err := cleaner.Exclusive(ctx, func() error {
		var err error
		stats, _, err = cleaner.Page(ctx, "")
		return err
	})
	if err != nil || !acquired {
		t.Fatalf("Exclusive() = %v, %v", acquired, err)
	}
	if stats.Deleted != 2 || stats.Repointed != 1 || stats.Referenced != 1 {
		t.Fatalf("stats = %+v, want two deleted, one repointed, one kept", stats)
	}
	want := []string{key(0, "w300.webp"), key(1, "w300.webp"), key(2, "original.webp")}
	if got := storedKeys(t, store); !slices.Equal(got, want) {
		t.Fatalf("remaining objects = %v, want %v", got, want)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT chapters::text FROM media_files WHERE id = $1`, fileID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	wantChapters := fmt.Sprintf(`[{"index": 0, "thumbnail_path": %q}, {"index": 1, "thumbnail_path": %q, "thumbnail_thumbhash": "h1"}, {"index": 2, "thumbnail_path": %q}]`,
		key(0, "w300.webp"), key(1, "w300.webp"), key(2, "original.webp"))
	if got != wantChapters {
		t.Fatalf("chapters = %s, want %s", got, wantChapters)
	}
}

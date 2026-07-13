package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestAudiobookRescanSkipRoundTrip pins the rescan fast path end to end:
// upserting a book's media_files with the parsed size/mtime must make
// audiobookFolderShouldSkip report the folder as unchanged on the next scan,
// and any size drift must force the full reconcile path again. Before the
// size/mtime wiring existed, every stored row had file_size=0 /
// file_modified_at=NULL, so the skip path could never fire and full-library
// rescans re-probed all ~240k books every time.
func TestAudiobookRescanSkipRoundTrip(t *testing.T) {
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
	contentID := fmt.Sprintf("audiobook-skip-%d", suffix)

	bookDir := filepath.Join(t.TempDir(), "Author", "Some Book (2020)")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	audioPath := filepath.Join(bookDir, "Some Book.mp3")
	if err := os.WriteFile(audioPath, []byte("not really audio"), 0o644); err != nil {
		t.Fatalf("write audio file: %v", err)
	}
	info, err := os.Stat(audioPath)
	if err != nil {
		t.Fatalf("stat audio file: %v", err)
	}

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', 'Skip Test', true) RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'audiobook', 'Some Book', 'matched', '{}'::text[])
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	s := &Scanner{
		fileRepo: NewFileRepository(pool),
		itemRepo: catalog.NewItemRepository(pool),
	}
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks"}

	book := &parsedAudiobook{
		Title: "Some Book",
		Files: []parsedAudiobookFile{{
			Path:      audioPath,
			Container: "mp3",
			Size:      info.Size(),
			ModTime:   info.ModTime(),
		}},
	}
	if err := s.upsertAudiobookMediaFiles(ctx, folder, contentID, bookDir, book); err != nil {
		t.Fatalf("upsert audiobook files: %v", err)
	}

	// Stored size/mtime must round-trip so the disk comparison can match.
	var storedSize int64
	var storedMod *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT file_size, file_modified_at FROM media_files WHERE file_path = $1
	`, audioPath).Scan(&storedSize, &storedMod); err != nil {
		t.Fatalf("read stored file row: %v", err)
	}
	if storedSize != info.Size() || storedMod == nil {
		t.Fatalf("stored size/mtime = %d/%v, want %d/non-nil", storedSize, storedMod, info.Size())
	}

	gotContentID, unchanged, err := s.audiobookFolderShouldSkip(ctx, folder, bookDir)
	if err != nil {
		t.Fatalf("skip check: %v", err)
	}
	if !unchanged || gotContentID != contentID {
		t.Fatalf("skip check = (%q, %v), want (%q, true)", gotContentID, unchanged, contentID)
	}

	// Any size drift must force the full reconcile path again.
	if err := os.WriteFile(audioPath, []byte("not really audio, but longer"), 0o644); err != nil {
		t.Fatalf("grow audio file: %v", err)
	}
	if _, unchanged, err := s.audiobookFolderShouldSkip(ctx, folder, bookDir); err != nil {
		t.Fatalf("skip check after change: %v", err)
	} else if unchanged {
		t.Fatal("skip check reported unchanged after the file grew")
	}
}

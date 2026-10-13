package audiobooks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// seedItemWithFile inserts one catalog item of itemType in a library of
// libraryType, with one file, and returns its content ID.
func seedItemWithFile(t *testing.T, pool *pgxpool.Pool, itemType, libraryType string) string {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("%s-files-fixture-%d", itemType, suffix)

	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ($1, $2) RETURNING id`,
		libraryType, fmt.Sprintf("Files Fixture %s %d", libraryType, suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed %s library: %v", libraryType, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, refresh_failures, episode_metadata_incomplete)
		VALUES ($1, $2, 'Files Fixture', '{}'::text[], 0, FALSE)`, contentID, itemType); err != nil {
		t.Fatalf("seed %s item: %v", itemType, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, now())`,
		contentID, folderID); err != nil {
		t.Fatalf("seed %s library membership: %v", itemType, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, content_id, file_hash, file_size, duration)
		VALUES ($1, $2, $3, $4, 1000, 60)`,
		folderID, "/files-fixture/"+contentID+".bin", contentID, "hash-"+contentID); err != nil {
		t.Fatalf("seed %s file: %v", itemType, err)
	}
	return contentID
}

// TestABSMediaFilesServeAudiobooksOnlyDB: the ABS file routes resolve files
// through GetMediaFiles, so a movie the profile can see must have none there,
// while an audiobook keeps its files.
func TestABSMediaFilesServeAudiobooksOnlyDB(t *testing.T) {
	pool := newClaimTestPool(t)
	store := &ABSMediaStore{Items: catalog.NewItemRepository(pool), Files: scanner.NewFileRepository(pool), Pool: pool}
	ctx := context.Background()
	unrestricted := catalog.AccessFilter{}

	movie := seedItemWithFile(t, pool, "movie", "movies")
	files, err := store.GetMediaFiles(ctx, movie, unrestricted)
	if err != nil {
		t.Fatalf("movie files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("movie files = %d, want none: the ABS file routes would stream a movie", len(files))
	}

	audiobook := seedItemWithFile(t, pool, "audiobook", "audiobooks")
	files, err = store.GetMediaFiles(ctx, audiobook, unrestricted)
	if err != nil {
		t.Fatalf("audiobook files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("audiobook files = %d, want 1", len(files))
	}
}

package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRebindKeepsHeldFirstSeen covers the live path of an arr movie upgrade:
// the replacement file first gets a provisional item with a fresh membership,
// then matching rebinds that item onto the existing movie by moving the
// membership, and a later upsert of the same membership is a no-op. The
// movie's held added date must survive all three steps.
func TestRebindKeepsHeldFirstSeen(t *testing.T) {
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
	movieID := fmt.Sprintf("movie-rebind-held-%d", suffix)
	skeletonID := fmt.Sprintf("local-rebind-held-%d", suffix)
	root := fmt.Sprintf("/rebind-held-%d/Alpha (2020)", suffix)
	old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("Rebind held %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieID, skeletonID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	exec(`
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path)
		VALUES ($1, 'movie', 'Alpha', 'matched', '{}'::text[], '', '', ''),
		       ($2, 'movie', 'Alpha', 'unmatched', '{}'::text[], '', '', '')
	`, movieID, skeletonID)

	// The old release was deleted: its row is missing and the membership is
	// gone, with the added date held on the row.
	exec(`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, missing_since)
		VALUES ($1, $2, $3, 1024, NOW())`, movieID, folderID, root+"/Alpha (2020) WEBDL-720p.mkv")
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, movieID, folderID, old)
	exec(`DELETE FROM media_item_libraries WHERE content_id = $1`, movieID)

	// The replacement arrives as a provisional item.
	exec(`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
		VALUES ($1, $2, $3, 2048)`, skeletonID, folderID, root+"/Alpha (2020) Bluray-1080p.mkv")
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, skeletonID, folderID)

	svc := &MetadataService{dbPool: pool}
	if err := svc.rebindItemToExistingItem(ctx, skeletonID, movieID, false); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if err := catalog.NewLibraryItemRepository(pool).Upsert(ctx, movieID, folderID, time.Now()); err != nil {
		t.Fatalf("upsert membership: %v", err)
	}

	var firstSeen time.Time
	if err := pool.QueryRow(ctx,
		`SELECT first_seen_at FROM media_item_libraries WHERE content_id = $1 AND media_folder_id = $2`,
		movieID, folderID).Scan(&firstSeen); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if !firstSeen.Equal(old) {
		t.Fatalf("first_seen_at after rebind = %v, want %v", firstSeen, old)
	}
	var held int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM media_files WHERE media_folder_id = $1 AND held_item_first_seen_at IS NOT NULL`,
		folderID).Scan(&held); err != nil {
		t.Fatalf("read held values: %v", err)
	}
	if held != 0 {
		t.Fatalf("held values left after restore: %d", held)
	}
}

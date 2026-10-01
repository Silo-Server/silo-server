package catalog

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestDeleteOrphanedItemsAndImageDirsKeepsRelinkedArtworkPostgres covers the
// window in reconcile between reading the orphan IDs and deleting them. A
// concurrent scan can link one of those items to a library in that window; the
// guarded DELETE then keeps it, and its artwork directories must not be
// reported for deletion. The test puts the item in that state directly: it is
// passed as an orphan but already has a membership again.
func TestDeleteOrphanedItemsAndImageDirsKeepsRelinkedArtworkPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var folderID int
	if err := tx.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies','orphan image dirs',true) RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
 INSERT INTO media_items(content_id,type,title,poster_path,backdrop_path) VALUES
 ('orphandirs-gone','movie','Gone','orphandirs/movies/1/poster/a.webp','orphandirs/movies/1/backdrop/b.webp'),
 ('orphandirs-relinked','movie','Relinked','orphandirs/movies/2/poster/a.webp','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES ('orphandirs-relinked',$1)`, folderID); err != nil {
		t.Fatal(err)
	}

	deleted, dirs, err := deleteOrphanedItemsAndImageDirs(ctx, tx, []string{"orphandirs-gone", "orphandirs-relinked"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(deleted, []string{"orphandirs-gone"}) {
		t.Fatalf("deleted = %q, want only the item with no membership", deleted)
	}
	slices.Sort(dirs)
	want := []string{"orphandirs/movies/1/backdrop/", "orphandirs/movies/1/poster/"}
	if !slices.Equal(dirs, want) {
		t.Fatalf("unreferenced image dirs = %q, want %q (the relinked item's poster directory must survive)", dirs, want)
	}

	var survivors int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM media_items WHERE content_id = 'orphandirs-relinked'`).Scan(&survivors); err != nil {
		t.Fatal(err)
	}
	if survivors != 1 {
		t.Fatalf("relinked item rows = %d, want 1", survivors)
	}
}

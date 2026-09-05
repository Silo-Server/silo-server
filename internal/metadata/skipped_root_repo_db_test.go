package metadata

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSkippedRootPageSearchBeforePagination(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	name := fmt.Sprintf("skipped-page-%d", time.Now().UnixNano())
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, name).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM skipped_media_roots WHERE media_folder_id=$1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folderID)
	}()
	repo := NewSkippedRootRepository(pool)
	for i := range 4 {
		if err := repo.Upsert(ctx, models.SkippedMediaRoot{MediaFolderID: folderID, RootPath: fmt.Sprintf("/fixture/%s/%d", name, i), Reason: "missing_folder_ids"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := repo.ListPage(ctx, name, 2, 0)
	if err != nil || len(first) != 2 {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := repo.ListPage(ctx, name, 2, 2)
	if err != nil || len(second) != 2 {
		t.Fatalf("second: %+v %v", second, err)
	}
	if first[0].RootPath == second[0].RootPath || first[1].RootPath == second[1].RootPath {
		t.Fatal("duplicate page rows")
	}
	found, err := repo.ListPage(ctx, second[1].RootPath, 1, 0)
	if err != nil || len(found) != 1 || found[0].RootPath != second[1].RootPath {
		t.Fatalf("search before page: %+v %v", found, err)
	}
	empty, err := repo.ListPage(ctx, name+"%", 2, 0)
	if err != nil || len(empty) != 0 {
		t.Fatalf("literal wildcard: %+v %v", empty, err)
	}
}

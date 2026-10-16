package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestCataloguedFilesUnderListsPresentFilesOfTheTitleDB pins the catalogued
// files that can vouch for local artwork in a symlinked title folder: present
// files of the title (episodes' files carry their series' content ID) under
// the folder, not a sibling folder sharing its name as a prefix, not a missing
// file, not another title's file.
func TestCataloguedFilesUnderListsPresentFilesOfTheTitleDB(t *testing.T) {
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
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`,
		fmt.Sprintf("catalogued-files-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID) })

	base := fmt.Sprintf("/catalogued-files-%d/library/Show", suffix)
	series := fmt.Sprintf("catalogued-series-%d", suffix)
	other := fmt.Sprintf("catalogued-other-%d", suffix)
	rows := []struct {
		contentID, path string
		missing         bool
	}{
		{series, base + "/Season 01/Show S01E01.mkv", false},
		{series, base + "/Season 01/Show S01E02.mkv", false},
		{series, base + "/Season 01/Show S01E03.mkv", true},
		{series, base + " (2)/Season 01/Show S01E01.mkv", false},
		{other, base + "/Season 01/Other S01E01.mkv", false},
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, missing_since)
			VALUES ($1, $2, $3, CASE WHEN $4 THEN NOW() END)`,
			row.contentID, folderID, row.path, row.missing); err != nil {
			t.Fatalf("seed file %q: %v", row.path, err)
		}
	}

	repo := NewFolderRepository(pool)
	got, err := repo.CataloguedFilesUnder(ctx, series, base, 10)
	if err != nil {
		t.Fatalf("list catalogued files: %v", err)
	}
	want := []string{base + "/Season 01/Show S01E01.mkv", base + "/Season 01/Show S01E02.mkv"}
	if !slices.Equal(got, want) {
		t.Fatalf("catalogued files = %q, want %q", got, want)
	}
	if got, err := repo.CataloguedFilesUnder(ctx, series, base+"/", 1); err != nil || len(got) != 1 {
		t.Fatalf("limited listing = %q, %v; want one file", got, err)
	}
}

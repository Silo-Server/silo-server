package scanner

import (
	"fmt"
	"testing"
	"time"
)

// TestFileRepositoryIsActiveFileDB pins the catalog half of a transcode
// node's source grant: the row id, its exact path, and not missing.
func TestFileRepositoryIsActiveFileDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Active file test")

	suffix := time.Now().UnixNano()
	activePath := fmt.Sprintf("/tmp/silo-active-file-%d.mkv", suffix)
	missingPath := fmt.Sprintf("/tmp/silo-missing-file-%d.mkv", suffix)
	var activeID, missingID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path) VALUES ($1, $2) RETURNING id`,
		folderID, activePath).Scan(&activeID); err != nil {
		t.Fatalf("seed active media file: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path, missing_since) VALUES ($1, $2, NOW()) RETURNING id`,
		folderID, missingPath).Scan(&missingID); err != nil {
		t.Fatalf("seed missing media file: %v", err)
	}

	repo := NewFileRepository(pool)
	for _, test := range []struct {
		name string
		id   int
		path string
		want bool
	}{
		{name: "active", id: activeID, path: activePath, want: true},
		{name: "missing", id: missingID, path: missingPath, want: false},
		{name: "another row's path", id: activeID, path: missingPath, want: false},
		{name: "another row at the path", id: missingID, path: activePath, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := repo.IsActiveFile(ctx, test.id, test.path)
			if err != nil {
				t.Fatalf("IsActiveFile: %v", err)
			}
			if got != test.want {
				t.Fatalf("IsActiveFile(%d, %q) = %v, want %v", test.id, test.path, got, test.want)
			}
		})
	}
}

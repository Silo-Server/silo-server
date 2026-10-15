package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFileRepositoryIsActiveFile(t *testing.T) {
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
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', 'Active Path Test', true)
		RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	activePath := fmt.Sprintf("/tmp/silo-active-path-%d.mkv", suffix)
	missingPath := fmt.Sprintf("/tmp/silo-missing-path-%d.mkv", suffix)
	var activeID, missingID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, missing_since)
		VALUES ($1, $2, NULL) RETURNING id
	`, folderID, activePath).Scan(&activeID); err != nil {
		t.Fatalf("seed active media file: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, missing_since)
		VALUES ($1, $2, NOW()) RETURNING id
	`, folderID, missingPath).Scan(&missingID); err != nil {
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
		{name: "other path", id: activeID, path: missingPath, want: false},
		{name: "unknown", id: activeID, path: fmt.Sprintf("/tmp/silo-unknown-path-%d.mkv", suffix), want: false},
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

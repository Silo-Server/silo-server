package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFileRepositoryIsActivePath(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, missing_since)
		VALUES ($1, $2, NULL), ($1, $3, NOW())
	`, folderID, activePath, missingPath); err != nil {
		t.Fatalf("seed media files: %v", err)
	}

	repo := NewFileRepository(pool)
	for _, test := range []struct {
		name string
		path string
		want bool
	}{
		{name: "active", path: activePath, want: true},
		{name: "missing", path: missingPath, want: false},
		{name: "unknown", path: fmt.Sprintf("/tmp/silo-unknown-path-%d.mkv", suffix), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := repo.IsActivePath(ctx, test.path)
			if err != nil {
				t.Fatalf("IsActivePath: %v", err)
			}
			if got != test.want {
				t.Fatalf("IsActivePath(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

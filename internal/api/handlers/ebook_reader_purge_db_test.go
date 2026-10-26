package handlers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Purging a deleted profile removes its reading progress, reader settings and
// annotations, and leaves another profile's on the same account.
func TestEbookReaderPurgeProfileDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("ebook-purge-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	contentID := fmt.Sprintf("ebook:purge-%d", userID)
	var folderID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('ebooks', $1) RETURNING id`, contentID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'ebook', 'Book')`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path, content_id) VALUES ($1, $2, $3) RETURNING id`,
		folderID, "/fixture/"+contentID+".epub", contentID).Scan(&fileID); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	for _, profileID := range []string{"deleted", "kept"} {
		for _, stmt := range []string{
			`INSERT INTO ebook_reader_progress (user_id, profile_id, content_id, file_id, location, progress, updated_at) VALUES ($1, $2, $3, $4, 'loc', 0.5, now())`,
			`INSERT INTO ebook_reader_config (user_id, profile_id, content_id) VALUES ($1, $2, $3)`,
			`INSERT INTO ebook_reader_annotations (id, user_id, profile_id, content_id, kind, location) VALUES ($2 || '-mark', $1, $2, $3, 'bookmark', 'loc')`,
		} {
			args := []any{userID, profileID, contentID}
			if strings.Contains(stmt, "$4") {
				args = append(args, fileID)
			}
			if _, err := pool.Exec(ctx, stmt, args...); err != nil {
				t.Fatalf("seed %s: %v", profileID, err)
			}
		}
	}

	if err := NewPGEbookReaderProgressStore(pool).PurgeProfile(ctx, userID, "deleted"); err != nil {
		t.Fatalf("PurgeProfile: %v", err)
	}
	for _, table := range []string{"ebook_reader_progress", "ebook_reader_config", "ebook_reader_annotations"} {
		for profileID, want := range map[string]int{"deleted": 0, "kept": 1} {
			var got int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE user_id = $1 AND profile_id = $2", userID, profileID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s rows of %s = %d, want %d", table, profileID, got, want)
			}
		}
	}
}

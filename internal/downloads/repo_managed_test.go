package downloads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// managedFixture is the seeded data a managed-entry authorization test needs:
// one user, two profiles on that same user_id, and a device per profile.
type managedFixture struct {
	pool      *pgxpool.Pool
	repo      *Repository
	userID    int
	profileA  string
	profileB  string
	deviceA   string
	deviceB   string
	contentID string
	fileID    int
}

func newDownloadsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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

	// Skip when the managed downloads reshape migration has not been applied.
	var col *string
	err = pool.QueryRow(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'downloads' AND column_name = 'device_id'`).Scan(&col)
	if errors.Is(err, pgx.ErrNoRows) || col == nil {
		t.Skip("downloads managed reshape migration has not been applied")
	}
	if err != nil {
		t.Fatalf("check downloads reshape: %v", err)
	}
	return pool
}

func seedManagedFixture(t *testing.T) managedFixture {
	t.Helper()
	ctx := context.Background()
	pool := newDownloadsTestPool(t)
	suffix := time.Now().UnixNano()

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`,
		fmt.Sprintf("Downloads Test %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}

	contentID := fmt.Sprintf("dl-content-%d", suffix)
	var fileID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
		 VALUES ($1, $2, $3, 1024) RETURNING id`,
		contentID, folderID, fmt.Sprintf("/tmp/downloads-test-%d.mp4", suffix),
	).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}

	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role, download_allowed) VALUES ($1, 'user', true) RETURNING id`,
		fmt.Sprintf("dluser-%d", suffix),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	profileA := fmt.Sprintf("dlp-a-%d", suffix)
	profileB := fmt.Sprintf("dlp-b-%d", suffix)
	for _, p := range []struct{ id, name string }{{profileA, "A"}, {profileB, "B"}} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, $3)`,
			p.id, userID, p.name,
		); err != nil {
			t.Fatalf("seed profile %s: %v", p.id, err)
		}
	}

	repo := NewRepository(pool)
	deviceA := fmt.Sprintf("dev-a-%d", suffix)
	deviceB := fmt.Sprintf("dev-b-%d", suffix)
	if err := repo.EnsureDevice(ctx, userID, profileA, deviceA, "Phone A", "android"); err != nil {
		t.Fatalf("ensure device A: %v", err)
	}
	if err := repo.EnsureDevice(ctx, userID, profileB, deviceB, "Phone B", "android"); err != nil {
		t.Fatalf("ensure device B: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM downloads WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_devices WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	return managedFixture{
		pool: pool, repo: repo, userID: userID,
		profileA: profileA, profileB: profileB, deviceA: deviceA, deviceB: deviceB,
		contentID: contentID, fileID: fileID,
	}
}

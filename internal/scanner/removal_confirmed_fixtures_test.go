package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixtures for removal_confirmed_test.go. #2817 removed them with the tests
// that first used them; the arr-removal tests here still need them.

// presentStateFixture seeds one folder holding a series episode file and an
// unrelated movie file, both present, with no memberships yet.
type presentStateFixture struct {
	pool          *pgxpool.Pool
	folderID      int
	seriesID      string
	episodeID     string
	unrelatedID   string
	targetPath    string
	unrelatedPath string
	firstSeen     time.Time
}

func seedPresentStateFixture(ctx context.Context, t *testing.T, label string) presentStateFixture {
	t.Helper()

	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	fx := presentStateFixture{
		pool:          pool,
		seriesID:      fmt.Sprintf("present-%s-series-%d", label, suffix),
		episodeID:     fmt.Sprintf("present-%s-episode-%d", label, suffix),
		unrelatedID:   fmt.Sprintf("present-%s-unrelated-%d", label, suffix),
		targetPath:    fmt.Sprintf("/tmp/present-%s-%d/episode.mkv", label, suffix),
		unrelatedPath: fmt.Sprintf("/tmp/present-%s-%d/unrelated.mkv", label, suffix),
		firstSeen:     time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', 'Present State Test', true)
		RETURNING id
	`).Scan(&fx.folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, fx.folderID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{fx.seriesID, fx.unrelatedID})
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path)
		VALUES
			($1, 'series', 'Target Series', 'matched', '{}'::text[], '', '', ''),
			($2, 'movie', 'Unrelated Movie', 'matched', '{}'::text[], '', '', '')
	`, fx.seriesID, fx.unrelatedID); err != nil {
		t.Fatalf("seed media items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, still_path)
		VALUES ($1, $2, 1, 1, 'Episode', '')
	`, fx.episodeID, fx.seriesID); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, media_folder_id, file_path, file_size,
			season_number, episode_number, created_at
		)
		VALUES
			($1, $2, $3, $4, 1024, 1, 1, $5),
			($6, NULL, $3, $7, 1024, NULL, NULL, NOW())
	`, fx.seriesID, fx.episodeID, fx.folderID, fx.targetPath, fx.firstSeen, fx.unrelatedID, fx.unrelatedPath); err != nil {
		t.Fatalf("seed media files: %v", err)
	}

	return fx
}

const replacedFileGrace = 24 * time.Hour

func (fx presentStateFixture) membershipFirstSeen(ctx context.Context, t *testing.T, table, idColumn, id string) *time.Time {
	t.Helper()
	var firstSeen *time.Time
	if err := fx.pool.QueryRow(ctx,
		`SELECT MAX(first_seen_at) FROM `+table+` WHERE `+idColumn+` = $1 AND media_folder_id = $2`,
		id, fx.folderID).Scan(&firstSeen); err != nil {
		t.Fatalf("read %s first_seen_at: %v", table, err)
	}
	return firstSeen
}

func (fx presentStateFixture) count(ctx context.Context, t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (fx presentStateFixture) exec(ctx context.Context, t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := fx.pool.Exec(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func assertTime(t *testing.T, label string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

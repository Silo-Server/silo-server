package notifications

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests pin the RecordAvailabilityForPaths / RecordItemAvailabilityForPaths
// scope semantics across the starts_with -> LIKE rewrite: only files under the
// scope paths contribute availability rows, including when a scope path
// contains LIKE wildcard characters.

func newReleaseRepoTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run DB-backed release repo tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedReleaseFolder(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', 'Release Scope Test', true)
		RETURNING id
	`).Scan(&id); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	return id
}

func TestRecordAvailabilityForPathsHonorsScope(t *testing.T) {
	ctx := context.Background()
	pool := newReleaseRepoTestPool(t)
	repo := NewReleaseRepository(pool)
	suffix := time.Now().UnixNano()
	folderID := seedReleaseFolder(t, pool)

	seriesID := fmt.Sprintf("series-relscope-%d", suffix)
	inScopeEp := fmt.Sprintf("ep-in-%d", suffix)
	outScopeEp := fmt.Sprintf("ep-out-%d", suffix)
	// The scope directory contains LIKE wildcards; the sibling directory is a
	// literal-text near-collision that '%'/'_' would wrongly match if the
	// prefix were not escaped.
	scopeDir := fmt.Sprintf("/rel-test-%d/Show_100%%/Season 01", suffix)
	siblingDir := fmt.Sprintf("/rel-test-%d/ShowX100Y/Season 01", suffix)

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM episode_availability WHERE episode_id = ANY($1)`, []string{inScopeEp, outScopeEp})
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'series', 'Release Scope Show', '{}'::text[])
	`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	for i, ep := range []struct {
		id  string
		dir string
	}{
		{inScopeEp, scopeDir},
		{outScopeEp, siblingDir},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number)
			VALUES ($1, $2, 1, $3)
		`, ep.id, seriesID, i+1); err != nil {
			t.Fatalf("seed episode %s: %v", ep.id, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, episode_id)
			VALUES ($1, $2, $3)
		`, folderID, fmt.Sprintf("%s/ep%d.mkv", ep.dir, i+1), ep.id); err != nil {
			t.Fatalf("seed file for %s: %v", ep.id, err)
		}
	}

	inserted, events, err := repo.RecordAvailabilityForPaths(ctx, folderID, []string{scopeDir}, false)
	if err != nil {
		t.Fatalf("RecordAvailabilityForPaths: %v", err)
	}
	if events != 0 {
		t.Fatalf("events = %d, want 0 (emitEvents=false)", events)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1 (only the in-scope episode)", inserted)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM episode_availability WHERE library_id = $1 AND episode_id = $2
	`, folderID, inScopeEp).Scan(&count); err != nil || count != 1 {
		t.Fatalf("in-scope availability rows = %d (err %v), want 1", count, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM episode_availability WHERE library_id = $1 AND episode_id = $2
	`, folderID, outScopeEp).Scan(&count); err != nil || count != 0 {
		t.Fatalf("out-of-scope availability rows = %d (err %v), want 0", count, err)
	}
}

func TestRecordItemAvailabilityForPathsHonorsScope(t *testing.T) {
	ctx := context.Background()
	pool := newReleaseRepoTestPool(t)
	repo := NewReleaseRepository(pool)
	suffix := time.Now().UnixNano()
	folderID := seedReleaseFolder(t, pool)

	inScopeMovie := fmt.Sprintf("movie-in-%d", suffix)
	outScopeMovie := fmt.Sprintf("movie-out-%d", suffix)
	scopeDir := fmt.Sprintf("/rel-test-%d/Movie_50%%", suffix)
	siblingDir := fmt.Sprintf("/rel-test-%d/MovieX50Y", suffix)

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM movie_availability WHERE item_id = ANY($1)`, []string{inScopeMovie, outScopeMovie})
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{inScopeMovie, outScopeMovie})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	for _, m := range []struct {
		id  string
		dir string
	}{
		{inScopeMovie, scopeDir},
		{outScopeMovie, siblingDir},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'movie', 'Scope Movie', '{}'::text[])
		`, m.id); err != nil {
			t.Fatalf("seed movie %s: %v", m.id, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, content_id)
			VALUES ($1, $2, $3)
		`, folderID, m.dir+"/movie.mkv", m.id); err != nil {
			t.Fatalf("seed file for %s: %v", m.id, err)
		}
	}

	movieKind := flatItemKinds[0]
	if movieKind.AvailabilityTable != movieAvailabilityTable {
		t.Fatalf("expected first flat kind to be movies, got %q", movieKind.Kind)
	}
	inserted, _, err := repo.RecordItemAvailabilityForPaths(ctx, movieKind, folderID, []string{scopeDir}, false)
	if err != nil {
		t.Fatalf("RecordItemAvailabilityForPaths: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1 (only the in-scope movie)", inserted)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM movie_availability WHERE library_id = $1 AND item_id = $2
	`, folderID, outScopeMovie).Scan(&count); err != nil || count != 0 {
		t.Fatalf("out-of-scope movie availability rows = %d (err %v), want 0", count, err)
	}
}

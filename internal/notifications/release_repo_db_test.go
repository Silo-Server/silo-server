package notifications

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// releaseFixture is one library with catalog rows in the migrated test
// database, keyed by a unique prefix so parallel runs do not collide.
type releaseFixture struct {
	pool      *pgxpool.Pool
	repo      *ReleaseRepository
	prefix    string
	libraryID int
}

func newReleaseFixture(t *testing.T, libraryType string) releaseFixture {
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

	nonce := time.Now().UnixNano()
	f := releaseFixture{
		pool:      pool,
		repo:      NewReleaseRepository(pool),
		prefix:    fmt.Sprintf("release-%d", nonce),
		libraryID: 800000 + int(nonce%90000),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_folders (id, type, name) VALUES ($1, $2, $3)`,
		f.libraryID, libraryType, f.prefix); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM release_events WHERE library_id = $1`, f.libraryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM episode_availability WHERE library_id = $1`, f.libraryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM movie_availability WHERE library_id = $1`, f.libraryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, f.libraryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1 || '%'`, f.prefix)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, f.libraryID)
	})
	return f
}

func (f releaseFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func (f releaseFixture) eventTargets(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT COALESCE(episode_id, item_id) FROM release_events WHERE library_id = $1`, f.libraryID)
	if err != nil {
		t.Fatalf("list release events: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan release event: %v", err)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// TestEpisodeReleasesSkipEpisodesAddedLongAgo covers the late match: an
// episode whose file sat in the library unmatched for weeks becomes available
// when a later scan links it, and must be recorded without a release. An
// episode whose metadata arrived a few days after its file is still news.
func TestEpisodeReleasesSkipEpisodesAddedLongAgo(t *testing.T) {
	f := newReleaseFixture(t, "series")
	series := f.prefix + "-series"
	oldFile, lateMetadata, justAdded := f.prefix+"-e1", f.prefix+"-e2", f.prefix+"-e3"
	f.exec(t, `INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'series', 'Series', '{}')`, series)
	f.exec(t, `INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1 || '-s1', $1, 1)`, series)
	f.exec(t, `INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title)
		VALUES ($1, $4, $4 || '-s1', 1, 1, 'E1'), ($2, $4, $4 || '-s1', 1, 2, 'E2'), ($3, $4, $4 || '-s1', 1, 3, 'E3')`,
		oldFile, lateMetadata, justAdded, series)
	f.exec(t, `INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
		VALUES ($1, $4, now() - interval '30 days'), ($2, $4, now() - interval '3 days'), ($3, $4, now())`,
		oldFile, lateMetadata, justAdded, f.libraryID)

	inserted, events, err := f.repo.RecordAvailabilityForLibrary(context.Background(), f.libraryID, true)
	if err != nil {
		t.Fatalf("RecordAvailabilityForLibrary: %v", err)
	}
	if inserted != 3 || events != 2 {
		t.Fatalf("inserted %d availability rows and %d events, want 3 and 2", inserted, events)
	}
	want := []string{lateMetadata, justAdded}
	slices.Sort(want)
	if got := f.eventTargets(t); !slices.Equal(got, want) {
		t.Fatalf("release events for %v, want %v", got, want)
	}
}

// TestMovieReleasesSkipMoviesAddedLongAgo applies the same rule to flat
// items, dated by their earliest present file in the library.
func TestMovieReleasesSkipMoviesAddedLongAgo(t *testing.T) {
	f := newReleaseFixture(t, "movies")
	oldFile, newFile, noFile := f.prefix+"-m1", f.prefix+"-m2", f.prefix+"-m3"
	f.exec(t, `INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'movie', 'M1', '{}'), ($2, 'movie', 'M2', '{}'), ($3, 'movie', 'M3', '{}')`,
		oldFile, newFile, noFile)
	f.exec(t, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $4), ($2, $4), ($3, $4)`,
		oldFile, newFile, noFile, f.libraryID)
	// A missing file does not date the item: only present files count.
	fileID := time.Now().UnixNano() % 1_000_000_000_000
	f.exec(t, `INSERT INTO media_files (id, media_folder_id, file_path, content_id, created_at, missing_since) VALUES
		($1, $4, $5 || '/m1.mkv', $2, now() - interval '30 days', NULL),
		($1 + 1, $4, $5 || '/m2.mkv', $3, now(), NULL),
		($1 + 2, $4, $5 || '/m2-old.mkv', $3, now() - interval '30 days', now())`,
		fileID, oldFile, newFile, f.libraryID, "/"+f.prefix)

	movies, ok := flatKindByString(EventKindMovie)
	if !ok {
		t.Fatal("movie kind is not registered")
	}
	inserted, events, err := f.repo.RecordItemAvailabilityForLibrary(context.Background(), movies, f.libraryID, true)
	if err != nil {
		t.Fatalf("RecordItemAvailabilityForLibrary: %v", err)
	}
	if inserted != 3 || events != 2 {
		t.Fatalf("inserted %d availability rows and %d events, want 3 and 2", inserted, events)
	}
	want := []string{newFile, noFile}
	slices.Sort(want)
	if got := f.eventTargets(t); !slices.Equal(got, want) {
		t.Fatalf("release events for %v, want %v", got, want)
	}
}

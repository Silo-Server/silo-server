package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// storedArtworkReleaseTestPool isolates the release in a schema of its own
// with the tables and columns it reads, so it neither sees nor writes the
// shared test database's catalog and setting.
func storedArtworkReleaseTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	schema := fmt.Sprintf("artwork_release_test_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `
	CREATE TABLE server_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE media_items (
		content_id TEXT PRIMARY KEY, type TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'matched', locked_fields INTEGER[] NOT NULL DEFAULT '{}',
		last_refreshed TIMESTAMPTZ,
		poster_path TEXT DEFAULT '', poster_source_path TEXT DEFAULT '', poster_thumbhash TEXT DEFAULT '',
		backdrop_path TEXT DEFAULT '', backdrop_source_path TEXT DEFAULT '', backdrop_thumbhash TEXT DEFAULT '',
		logo_path TEXT DEFAULT '', logo_source_path TEXT DEFAULT '',
		banner_path TEXT DEFAULT '', banner_source_path TEXT DEFAULT '', banner_thumbhash TEXT DEFAULT '',
		thumb_path TEXT DEFAULT '', thumb_source_path TEXT DEFAULT '', thumb_thumbhash TEXT DEFAULT '',
		clearart_path TEXT DEFAULT '', clearart_source_path TEXT DEFAULT '',
		disc_path TEXT DEFAULT '', disc_source_path TEXT DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	CREATE TABLE media_item_localizations (
		content_id TEXT NOT NULL, language TEXT NOT NULL,
		poster_path TEXT DEFAULT '', poster_source_path TEXT DEFAULT '', poster_thumbhash TEXT DEFAULT '',
		backdrop_path TEXT DEFAULT '', backdrop_source_path TEXT DEFAULT '', backdrop_thumbhash TEXT DEFAULT '',
		logo_path TEXT DEFAULT '', logo_source_path TEXT DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (content_id, language)
	);
	CREATE TABLE media_item_backdrops (content_id TEXT NOT NULL, path TEXT DEFAULT '');
	CREATE TABLE seasons (
		content_id TEXT PRIMARY KEY, series_id TEXT NOT NULL,
		poster_path TEXT DEFAULT '', poster_source_path TEXT DEFAULT '', poster_thumbhash TEXT DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	CREATE TABLE season_localizations (
		season_content_id TEXT NOT NULL, language TEXT NOT NULL,
		poster_path TEXT DEFAULT '', poster_source_path TEXT DEFAULT '', poster_thumbhash TEXT DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (season_content_id, language)
	);
	CREATE TABLE episodes (
		content_id TEXT PRIMARY KEY, series_id TEXT NOT NULL,
		still_path TEXT DEFAULT '', still_source_path TEXT DEFAULT '', still_thumbhash TEXT DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	CREATE TABLE metadata_refresh_debt (
		target_type TEXT NOT NULL, content_id TEXT NOT NULL,
		priority INTEGER NOT NULL DEFAULT 0, reason_mask BIGINT NOT NULL DEFAULT 0,
		next_refresh_at TIMESTAMPTZ NOT NULL, claimed_at TIMESTAMPTZ,
		lease_expires_at TIMESTAMPTZ, last_attempt_at TIMESTAMPTZ, last_success_at TIMESTAMPTZ,
		attempt_count INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (target_type, content_id)
	)`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func newTestStoredArtworkReleaser(pool *pgxpool.Pool) *StoredArtworkReleaser {
	r := NewStoredArtworkReleaser(pool)
	// Small pages exercise paging; the advisory lock is cluster-wide, and
	// other tests' passes must not skip this one.
	r.pageSize = 2
	r.lock = func(context.Context) (func(), bool, error) { return func() {}, true, nil }
	return r
}

// With caching off, a pass points every stored copy of provider artwork on an
// unlocked movie or series back at its provider source, keeping its
// thumbhash; copies with no provider source stay and their matched item gets
// refresh debt (unless refreshed this week); locked items and books are
// untouched; a second pass changes nothing; and with caching on a pass does
// nothing.
func TestStoredArtworkReleaseDB(t *testing.T) {
	pool := storedArtworkReleaseTestPool(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	const (
		stored = "tmdb/movies/1/poster/original.r1.webp"
		source = "tmdb://poster/p.jpg"
		local  = "local/movies/1/backdrop/original.r1.webp"
	)
	exec(`INSERT INTO server_settings VALUES ('metadata.cache_images', 'true')`)
	// movie-a: stored provider poster, stored local backdrop, never refreshed.
	exec(`INSERT INTO media_items (content_id, type, poster_path, poster_source_path, poster_thumbhash,
		backdrop_path, backdrop_source_path, backdrop_thumbhash) VALUES
		('movie-a', 'movie', $1, $2, 'hash', $3, 'file:///m/a/fanart.jpg?v=1-1', 'bhash')`, stored, source, local)
	// movie-b: locked artwork; movie-c: an audiobook; movie-d: refreshed today
	// with a stored local poster; movie-e: extra backdrops only.
	exec(`INSERT INTO media_items (content_id, type, locked_fields, poster_path, poster_source_path) VALUES
		('movie-b', 'movie', '{10}', $1, $2), ('book-c', 'audiobook', '{}', $1, $2)`, stored, source)
	exec(`INSERT INTO media_items (content_id, type, last_refreshed, poster_path, poster_source_path) VALUES
		('movie-d', 'movie', NOW(), $1, 'file:///m/d/poster.jpg?v=1-1'), ('movie-e', 'movie', NOW() - interval '30 days', '', '')`, local)
	exec(`INSERT INTO media_item_backdrops VALUES ('movie-e', 'local/x')`)
	// movie-f: unmatched, with a stored local poster: no provider can help.
	exec(`INSERT INTO media_items (content_id, type, status, poster_path, poster_source_path) VALUES
		('movie-f', 'movie', 'unmatched', $1, 'file:///m/f/poster.jpg?v=1-1')`, local)
	exec(`INSERT INTO media_item_localizations (content_id, language, poster_path, poster_source_path, poster_thumbhash)
		VALUES ('movie-a', 'fr', $1, 'tmdb://poster/fr.jpg', 'frhash'), ('movie-b', 'fr', $1, 'tmdb://poster/fr.jpg', 'frhash'),
		('movie-a', 'de', $1, '', 'dehash')`, stored)
	// movie-g: a storage recovery cleared its logo, which had no source.
	exec(`INSERT INTO media_items (content_id, type, last_refreshed, poster_path, poster_source_path, backdrop_path, backdrop_source_path)
		VALUES ('movie-g', 'movie', NULL, 'tmdb://poster/g.jpg', 'tmdb://poster/g.jpg', 'tmdb://backdrop/g.jpg', 'tmdb://backdrop/g.jpg')`)
	exec(`INSERT INTO media_items (content_id, type) VALUES ('series-s', 'series')`)
	exec(`INSERT INTO seasons (content_id, series_id, poster_path, poster_source_path) VALUES ('season-1', 'series-s', $1, 'tvdb://s1.jpg')`, stored)
	exec(`INSERT INTO season_localizations (season_content_id, language, poster_path, poster_source_path) VALUES ('season-1', 'de', $1, 'tvdb://s1de.jpg')`, stored)
	exec(`INSERT INTO episodes (content_id, series_id, still_path, still_source_path) VALUES
		('episode-1', 'series-s', $1, 'https://img.example/e1.jpg'), ('episode-2', 'series-s', $1, 'file:///m/s/e2-thumb.jpg?v=1-1')`, stored)

	r := newTestStoredArtworkReleaser(pool)
	stats, err := r.Run(ctx)
	if err != nil || stats.Released != 0 || stats.RefreshesQueued != 0 {
		t.Fatalf("caching on: stats = %+v, err = %v; want nothing done", stats, err)
	}

	exec(`UPDATE server_settings SET value = 'false'`)
	stats, err = r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// movie-a, its fr and de localizations, season-1, its de localization,
	// episode-1.
	if stats.Released != 6 {
		t.Errorf("released = %d, want 6", stats.Released)
	}
	text := func(sql string, args ...any) string {
		t.Helper()
		var v string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	for name, got := range map[string][2]string{
		"movie-a poster":        {text(`SELECT poster_path || '|' || poster_thumbhash FROM media_items WHERE content_id = 'movie-a'`), source + "|hash"},
		"movie-a backdrop":      {text(`SELECT backdrop_path || '|' || backdrop_thumbhash FROM media_items WHERE content_id = 'movie-a'`), local + "|bhash"},
		"movie-a fr poster":     {text(`SELECT poster_path || '|' || poster_thumbhash FROM media_item_localizations WHERE content_id = 'movie-a' AND language = 'fr'`), "tmdb://poster/fr.jpg|frhash"},
		"movie-a de poster":     {text(`SELECT poster_path FROM media_item_localizations WHERE content_id = 'movie-a' AND language = 'de'`), ""},
		"movie-b poster":        {text(`SELECT poster_path FROM media_items WHERE content_id = 'movie-b'`), stored},
		"movie-b fr poster":     {text(`SELECT poster_path FROM media_item_localizations WHERE content_id = 'movie-b'`), stored},
		"book-c poster":         {text(`SELECT poster_path FROM media_items WHERE content_id = 'book-c'`), stored},
		"season-1 poster":       {text(`SELECT poster_path FROM seasons`), "tvdb://s1.jpg"},
		"season-1 de poster":    {text(`SELECT poster_path FROM season_localizations`), "tvdb://s1de.jpg"},
		"episode-1 still":       {text(`SELECT still_path FROM episodes WHERE content_id = 'episode-1'`), "https://img.example/e1.jpg"},
		"episode-2 local still": {text(`SELECT still_path FROM episodes WHERE content_id = 'episode-2'`), stored},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
	// movie-a (local backdrop), movie-e (extra backdrops), movie-g (cleared
	// logo) and series-s (local still); not movie-d, refreshed today, nor
	// movie-f, unmatched.
	debts := text(`SELECT string_agg(content_id || ':' || reason_mask, ',' ORDER BY content_id) FROM metadata_refresh_debt WHERE target_type = 'item'`)
	if want := "movie-a:64,movie-e:64,movie-g:64,series-s:64"; debts != want {
		t.Errorf("refresh debt = %q, want %q", debts, want)
	}

	stats, err = r.Run(ctx)
	if err != nil || stats.Released != 0 {
		t.Fatalf("second pass: stats = %+v, err = %v; want nothing released", stats, err)
	}

	// A copy published after a pass, while caching is back on, stays.
	exec(`UPDATE server_settings SET value = 'true'`)
	exec(`UPDATE media_items SET poster_path = $1 WHERE content_id = 'movie-a'`, stored)
	if _, err := r.Run(ctx); err != nil {
		t.Fatalf("Run with caching on: %v", err)
	}
	if got := text(`SELECT poster_path FROM media_items WHERE content_id = 'movie-a'`); got != stored {
		t.Errorf("movie-a poster with caching on = %q, want the stored copy kept", got)
	}
}

// A release that has to wait for an Edit Metadata selection in progress reads
// the lock the selection commits, so the selected copy stays.
func TestStoredArtworkReleaseWaitsForSelectionLockDB(t *testing.T) {
	pool := storedArtworkReleaseTestPool(t)
	ctx := t.Context()
	const selected = "tmdb/movies/1/poster/original.manual.webp"
	if _, err := pool.Exec(ctx, `INSERT INTO server_settings VALUES ('metadata.cache_images', 'false');
		INSERT INTO media_items (content_id, type, poster_path, poster_source_path)
		VALUES ('movie-a', 'movie', 'tmdb/movies/1/poster/original.auto.webp', 'tmdb://poster/auto.jpg')`); err != nil {
		t.Fatal(err)
	}
	// The selection, as PublishArtworkSelection writes it: the target row
	// first, then the lock, in one transaction.
	selection, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selection.Rollback(context.Background()) }()
	if _, err := selection.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = 'movie-a' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := selection.Exec(ctx, `UPDATE media_items SET poster_path = $1, poster_source_path = 'tmdb://poster/manual.jpg',
		locked_fields = array_append(locked_fields, 10) WHERE content_id = 'movie-a'`, selected); err != nil {
		t.Fatal(err)
	}

	r := newTestStoredArtworkReleaser(pool)
	done := make(chan error, 1)
	go func() {
		_, err := r.releasePage(context.Background(), storedArtworkReleaseTables()[0], []string{"movie-a"})
		done <- err
	}()
	// Wait until the release is blocked on the selection's row lock.
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%media_items t %')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("release finished before the selection committed: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := selection.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("releasePage: %v", err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = 'movie-a'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != selected {
		t.Fatalf("poster = %q, want the locked selection %q kept", got, selected)
	}
}

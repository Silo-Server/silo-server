package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func imageCacheQueueTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// parkFailedImageCacheJob forces a job into the terminal state a spent attempt
// budget or a permanent tombstone would leave behind, so the re-admission gate
// can be exercised without waiting out a real backoff.
func parkFailedImageCacheJob(t *testing.T, pool *pgxpool.Pool, contentID string, park time.Duration) {
	t.Helper()
	tag, err := pool.Exec(context.Background(), `
		UPDATE metadata_image_cache_jobs
		SET status = 'failed',
			attempt_count = $2,
			next_attempt_at = NOW() + $3::interval,
			last_error = 'test parked failure'
		WHERE target_type = 'item'
		  AND target_content_id = $1
		  AND image_type = 'poster'
		  AND target_language = ''
	`, contentID, imageCacheMaxAttempts, intervalLiteral(park))
	if err != nil {
		t.Fatalf("park failed job: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("park failed job affected %d rows, want 1", tag.RowsAffected())
	}
}

func readImageCacheJobState(t *testing.T, pool *pgxpool.Pool, contentID string) (string, int) {
	t.Helper()
	var status string
	var attempts int
	if err := pool.QueryRow(context.Background(), `
		SELECT status, attempt_count
		FROM metadata_image_cache_jobs
		WHERE target_type = 'item'
		  AND target_content_id = $1
		  AND image_type = 'poster'
		  AND target_language = ''
	`, contentID).Scan(&status, &attempts); err != nil {
		t.Fatalf("read job state: %v", err)
	}
	return status, attempts
}

// TestImageCacheFailedJobReadmission covers the re-admission gate shared by the
// enqueue upsert and catalog discovery: a job parked past the recovery window is
// a tombstone and stays put for an unchanged source, while a job that merely ran
// out of attempts comes back once its cooldown has elapsed.
func TestImageCacheFailedJobReadmission(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	ctx := context.Background()
	repo := NewImageCacheJobRepository(pool)

	newJob := func(t *testing.T, sourcePath string) string {
		t.Helper()
		contentID := fmt.Sprintf("image-cache-readmit-%d", time.Now().UnixNano())
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = $1`, contentID)
		})
		if err := repo.Enqueue(ctx, EnqueueImageCacheJobInput{
			TargetType:      ImageCacheTargetItem,
			TargetContentID: contentID,
			SourcePath:      sourcePath,
			ImageType:       ImageCacheImagePoster,
			ContentType:     "movie",
		}); err != nil {
			t.Fatalf("enqueue job: %v", err)
		}
		return contentID
	}

	const sourcePath = "https://image.tmdb.org/t/p/original/readmit.jpg"

	t.Run("attempt exhausted job revives after its cooldown", func(t *testing.T) {
		contentID := newJob(t, sourcePath)
		parkFailedImageCacheJob(t, pool, contentID, -time.Minute)

		if err := repo.Enqueue(ctx, EnqueueImageCacheJobInput{
			TargetType:      ImageCacheTargetItem,
			TargetContentID: contentID,
			SourcePath:      sourcePath,
			ImageType:       ImageCacheImagePoster,
			ContentType:     "movie",
		}); err != nil {
			t.Fatalf("re-enqueue job: %v", err)
		}

		status, attempts := readImageCacheJobState(t, pool, contentID)
		if status != ImageCacheStatusQueued {
			t.Fatalf("status = %q, want %q: an attempt-exhausted job must be recoverable", status, ImageCacheStatusQueued)
		}
		if attempts != 0 {
			t.Fatalf("attempt_count = %d, want 0", attempts)
		}
	})

	t.Run("tombstoned job stays parked for an unchanged source", func(t *testing.T) {
		contentID := newJob(t, sourcePath)
		parkFailedImageCacheJob(t, pool, contentID, imageCachePermanentPark)

		if err := repo.Enqueue(ctx, EnqueueImageCacheJobInput{
			TargetType:      ImageCacheTargetItem,
			TargetContentID: contentID,
			SourcePath:      sourcePath,
			ImageType:       ImageCacheImagePoster,
			ContentType:     "movie",
		}); err != nil {
			t.Fatalf("re-enqueue job: %v", err)
		}

		status, attempts := readImageCacheJobState(t, pool, contentID)
		if status != ImageCacheStatusFailed {
			t.Fatalf("status = %q, want %q: a tombstoned job must not be retried", status, ImageCacheStatusFailed)
		}
		if attempts != imageCacheMaxAttempts {
			t.Fatalf("attempt_count = %d, want %d", attempts, imageCacheMaxAttempts)
		}
	})

	t.Run("tombstoned job revives when the source changes", func(t *testing.T) {
		contentID := newJob(t, sourcePath)
		parkFailedImageCacheJob(t, pool, contentID, imageCachePermanentPark)

		if err := repo.Enqueue(ctx, EnqueueImageCacheJobInput{
			TargetType:      ImageCacheTargetItem,
			TargetContentID: contentID,
			SourcePath:      "https://image.tmdb.org/t/p/original/replacement.jpg",
			ImageType:       ImageCacheImagePoster,
			ContentType:     "movie",
		}); err != nil {
			t.Fatalf("re-enqueue job with new source: %v", err)
		}

		status, attempts := readImageCacheJobState(t, pool, contentID)
		if status != ImageCacheStatusQueued {
			t.Fatalf("status = %q, want %q: a new source must clear the tombstone", status, ImageCacheStatusQueued)
		}
		if attempts != 0 {
			t.Fatalf("attempt_count = %d, want 0", attempts)
		}
	})
}

func TestImageCacheDiscoveryQueriesExplain(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	ctx := context.Background()

	for surface := 0; surface < imageCacheDiscoverySurfaceCount; surface++ {
		query, args := imageCacheDiscoveryQuery(imageCacheDiscoveryCursor{Surface: surface}, 1000)
		if _, err := pool.Exec(ctx, "EXPLAIN "+query, args...); err != nil {
			t.Fatalf("EXPLAIN discovery surface %d: %v", surface, err)
		}
	}
}

// TestImageCacheLocalizedDiscoveryPagesBoundedSourceRows seeds a populated
// localization catalog and walks it the way the processor does. Every page
// must read one bounded slice of native keys, so a backfill whose candidates
// are all queued or parked still finishes in rows/limit cheap pages instead of
// rescanning the remaining catalog on every page.
func TestImageCacheLocalizedDiscoveryPagesBoundedSourceRows(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	ctx := context.Background()
	repo := NewImageCacheJobRepository(pool)

	const (
		items     = 1000
		languages = 2
		limit     = 100
		rows      = items * languages
	)
	prefix := fmt.Sprintf("zz-image-cache-discovery-%d-", time.Now().UnixNano())
	cleanup := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM metadata_image_cache_jobs WHERE target_content_id LIKE $1 || '%'`, prefix)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1 || '%'`, prefix)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, tmdb_id)
		SELECT $1 || lpad(i::text, 5, '0'), 'movie', 'Discovery ' || i, i::text
		FROM generate_series(0, $2 - 1) AS i
	`, prefix, items); err != nil {
		t.Fatalf("seed media items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_localizations (content_id, language, poster_source_path, poster_path)
		SELECT mi.content_id, lang, 'https://image.tmdb.org/t/p/original/' || mi.tmdb_id || '-' || lang || '.jpg', ''
		FROM media_items mi
		CROSS JOIN unnest(ARRAY['de', 'fr']) AS lang
		WHERE mi.content_id LIKE $1 || '%'
	`, prefix); err != nil {
		t.Fatalf("seed localizations: %v", err)
	}
	// A full page of already cached rows in the middle of the surface. The key
	// page still returns those rows as non-candidates, so they must advance the
	// cursor without looking like a short final page.
	if _, err := pool.Exec(ctx, `
		UPDATE media_item_localizations
		SET poster_path = 'tmdb/movies/cached.webp'
		WHERE content_id >= $1 || '00100' AND content_id < $1 || '00150'
	`, prefix); err != nil {
		t.Fatalf("mark cached localizations: %v", err)
	}
	for _, table := range []string{"media_items", "media_item_localizations", "metadata_image_cache_jobs"} {
		if _, err := pool.Exec(ctx, "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}

	type walkResult struct {
		pages      int
		scanned    int
		discovered int
	}
	// The cursor starts just before the seeded prefix and the walk stops once it
	// leaves it, so rows that other tests left in a shared database don't count.
	walk := func(t *testing.T) walkResult {
		t.Helper()
		var result walkResult
		cursor := imageCacheDiscoveryCursor{Surface: 3, Key: prefix}
		for {
			page, err := repo.EnqueueExistingProviderArtwork(ctx, cursor, limit)
			if err != nil {
				t.Fatalf("discovery page %d: %v", result.pages, err)
			}
			result.pages++
			if page.Scanned > limit {
				t.Fatalf("page %d scanned %d rows, want at most %d", result.pages, page.Scanned, limit)
			}
			result.scanned += page.Scanned
			result.discovered += page.Discovered
			if page.Next.Surface != 3 || !strings.HasPrefix(page.Next.Key, prefix) {
				return result
			}
			if page.Next.Key < cursor.Key || (page.Next.Key == cursor.Key && page.Next.Subkey <= cursor.Subkey) {
				t.Fatalf("cursor did not advance: %+v -> %+v", cursor, page.Next)
			}
			if page.Scanned < limit {
				t.Fatalf("page %d ended the surface with %d rows before the seeded catalog was exhausted", result.pages, page.Scanned)
			}
			cursor = page.Next
			if result.pages > rows {
				t.Fatalf("discovery did not terminate")
			}
		}
	}
	countJobs := func(t *testing.T, where string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM metadata_image_cache_jobs
			WHERE target_type = 'item_localization' AND image_type = 'poster'
			  AND target_content_id LIKE $1 || '%' AND `+where, prefix).Scan(&n); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		return n
	}

	first := walk(t)
	const cachedRows = 50 * languages
	if first.pages < rows/limit || first.pages > rows/limit+1 {
		t.Fatalf("first sweep used %d pages, want about %d", first.pages, rows/limit)
	}
	if got, want := countJobs(t, "true"), rows-cachedRows; got != want {
		t.Fatalf("first sweep queued %d localized jobs, want %d (cached page must be skipped, not end the surface)", got, want)
	}

	// Park every job in a seven-day cooldown, the idle state from the review:
	// nothing is eligible, yet the sweep must still read only bounded pages.
	if _, err := pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs
		SET status = 'failed', attempt_count = $2, next_attempt_at = NOW() + interval '7 days'
		WHERE target_content_id LIKE $1 || '%'
	`, prefix, imageCacheMaxAttempts); err != nil {
		t.Fatalf("park jobs: %v", err)
	}
	idle := walk(t)
	if idle.discovered != 0 {
		t.Fatalf("idle sweep discovered %d rows with every job parked", idle.discovered)
	}
	if idle.pages != first.pages {
		t.Fatalf("idle sweep used %d pages, first sweep used %d", idle.pages, first.pages)
	}
	if got := countJobs(t, "status <> 'failed'"); got != 0 {
		t.Fatalf("idle sweep re-queued %d parked jobs", got)
	}

	// Rows read: from a cursor in the middle of the catalog, the localization
	// scan must stop after one page of keys rather than read the remainder.
	mid := imageCacheDiscoveryCursor{Surface: 3, Key: prefix + "00500", Subkey: "de"}
	query, args := imageCacheDiscoveryQuery(mid, limit)
	var planJSON []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&planJSON); err != nil {
		t.Fatalf("explain analyze mid-catalog page: %v", err)
	}
	var plans []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal(planJSON, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode plan: %v", err)
	}
	var localizationRows float64
	var visit func(node map[string]any)
	visit = func(node map[string]any) {
		if node["Relation Name"] == "media_item_localizations" {
			localizationRows += asFloat(node["Actual Rows"]) + asFloat(node["Rows Removed by Filter"])
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			visit(child.(map[string]any))
		}
	}
	visit(plans[0].Plan)
	t.Logf("mid-catalog page read %.0f localization rows", localizationRows)
	if localizationRows == 0 || localizationRows > limit {
		t.Fatalf("mid-catalog page read %.0f localization rows, want 1..%d:\n%s", localizationRows, limit, planJSON)
	}
}

func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

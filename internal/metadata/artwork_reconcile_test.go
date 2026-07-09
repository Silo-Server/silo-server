package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeObjectChecker treats every key as present unless listed in missing or
// erroring. Defaulting to present keeps sweeps over rows seeded by other
// tests in the shared test database side-effect free.
type fakeObjectChecker struct {
	mu       sync.Mutex
	missing  map[string]bool
	erroring map[string]bool
	errorAll bool
	checked  map[string]int
}

func (f *fakeObjectChecker) Bucket() string { return "test-bucket" }

func (f *fakeObjectChecker) ObjectExists(_ context.Context, _ string, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checked == nil {
		f.checked = map[string]int{}
	}
	f.checked[key]++
	if f.errorAll || f.erroring[key] {
		return false, errors.New("simulated storage error")
	}
	return !f.missing[key], nil
}

func TestShouldBulkReset(t *testing.T) {
	if shouldBulkReset(0, 0) {
		t.Fatal("empty probe must not trigger bulk reset")
	}
	if shouldBulkReset(100, 94) {
		t.Fatal("94% missing is below the bulk threshold")
	}
	if !shouldBulkReset(100, 95) {
		t.Fatal("95% missing must trigger bulk reset")
	}
	// A probe thinned below the minimum successful-sample bar (transport
	// errors, tiny catalog) must take the safe per-row path even at a 100%
	// miss rate — a handful of surviving 404s is not a mandate to bulk-reset.
	if shouldBulkReset(artworkReconcileBulkMinSample-1, artworkReconcileBulkMinSample-1) {
		t.Fatal("below-minimum sample must not trigger bulk reset")
	}
	if !shouldBulkReset(artworkReconcileBulkMinSample, artworkReconcileBulkMinSample) {
		t.Fatal("all-missing probe at the minimum sample size must trigger bulk reset")
	}
}

func TestArtworkReconcileVerifySweep(t *testing.T) {
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
	id := func(name string) string { return fmt.Sprintf("arc-%s-%d", name, suffix) }
	key := func(name string) string { return fmt.Sprintf("tmdb/movies/arc-%d/%s/original.webp", suffix, name) }

	// Four items covering the sweep verdicts: intact, missing with a provider
	// source, missing with an upload source, and an uncached provider URL.
	seedItem := func(contentID, posterPath, posterSource string) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path, last_refreshed)
			VALUES ($1, 'movie', 'ARC Test', 'matched', '{}'::text[], $2, $3, NOW())
		`, contentID, posterPath, posterSource); err != nil {
			t.Fatalf("seed item %s: %v", contentID, err)
		}
	}
	seedItem(id("intact"), key("intact"), "https://img.example/intact.jpg")
	seedItem(id("missing"), key("missing"), "https://img.example/missing.jpg")
	seedItem(id("upload"), key("upload"), "upload://admin/poster.jpg")
	seedItem(id("uncached"), "https://img.example/direct.jpg", "https://img.example/direct.jpg")

	var fileID int64
	chapters := fmt.Sprintf(
		`[{"index":0,"title":"One","thumbnail_path":%q,"thumbnail_thumbhash":"aGFzaA==","custom":"kept"},`+
			`{"index":1,"title":"Two","thumbnail_path":%q,"thumbnail_thumbhash":"aGFzaA==","thumbnail_failed_at":"2026-01-01T00:00:00Z"}]`,
		key("chapter-intact"), key("chapter-missing"),
	)
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled, poster_path) VALUES ('movies', 'ARC Folder', true, $1) RETURNING id`,
		fmt.Sprintf("library-posters/arc-%d.png", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, chapters)
		VALUES ($1, $2, $3, $4::jsonb) RETURNING id
	`, id("intact"), folderID, fmt.Sprintf("/arc-%d/movie.mkv", suffix), chapters).Scan(&fileID); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_collections (id, library_id, slug, title, collection_type, poster_url, poster_thumbhash, poster_from_template)
		VALUES ($1, $2, $1, 'ARC Collection', 'manual', $3, 'aGFzaA==', TRUE)
	`, id("coll"), folderID, key("coll")); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM library_collections WHERE id = $1`, id("coll"))
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
		for _, name := range []string{"intact", "missing", "upload", "uncached"} {
			_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, id(name))
		}
	})

	checker := &fakeObjectChecker{missing: map[string]bool{
		key("missing"):         true,
		key("upload"):          true,
		key("chapter-missing"): true,
		key("coll"):            true,
		fmt.Sprintf("library-posters/arc-%d.png", suffix): true,
	}}
	stats, err := NewArtworkCacheReconciler(pool, checker).Run(ctx, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Mode != "verify" {
		t.Fatalf("Mode = %q, want verify (fake checker defaults to present)", stats.Mode)
	}

	var posterPath string
	var lastRefreshed *time.Time
	mustScanItem := func(contentID string) (string, *time.Time) {
		if err := pool.QueryRow(ctx,
			`SELECT poster_path, last_refreshed FROM media_items WHERE content_id = $1`, contentID,
		).Scan(&posterPath, &lastRefreshed); err != nil {
			t.Fatalf("read item %s: %v", contentID, err)
		}
		return posterPath, lastRefreshed
	}

	if got, _ := mustScanItem(id("intact")); got != key("intact") {
		t.Fatalf("intact poster_path = %q, want untouched %q", got, key("intact"))
	}
	if got, _ := mustScanItem(id("missing")); got != "https://img.example/missing.jpg" {
		t.Fatalf("missing poster_path = %q, want reset to provider source", got)
	}
	if got, refreshed := mustScanItem(id("upload")); got != "" || refreshed != nil {
		t.Fatalf("upload poster_path = %q (last_refreshed %v), want cleared with last_refreshed NULL", got, refreshed)
	}
	if got, _ := mustScanItem(id("uncached")); got != "https://img.example/direct.jpg" {
		t.Fatalf("uncached poster_path = %q, want untouched provider URL", got)
	}
	if checker.checked["https://img.example/direct.jpg"] != 0 {
		t.Fatal("provider URLs must not be HEAD-checked")
	}

	var rawChapters string
	var retryAfter *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT chapters::text, chapter_thumbnail_retry_after FROM media_files WHERE id = $1`, fileID,
	).Scan(&rawChapters, &retryAfter); err != nil {
		t.Fatalf("read chapters: %v", err)
	}
	assertContains := func(s, substr, what string) {
		t.Helper()
		if !strings.Contains(s, substr) {
			t.Fatalf("%s: %q not found in %s", what, substr, s)
		}
	}
	assertContains(rawChapters, key("chapter-intact"), "intact chapter thumbnail kept")
	assertContains(rawChapters, `"kept"`, "unknown chapter fields preserved")
	if strings.Contains(rawChapters, key("chapter-missing")) || strings.Contains(rawChapters, "thumbnail_failed_at") {
		t.Fatalf("missing chapter thumbnail not cleared: %s", rawChapters)
	}
	if retryAfter != nil {
		t.Fatal("chapter_thumbnail_retry_after not cleared")
	}

	var collPoster, collHash string
	var fromTemplate bool
	if err := pool.QueryRow(ctx,
		`SELECT poster_url, poster_thumbhash, poster_from_template FROM library_collections WHERE id = $1`, id("coll"),
	).Scan(&collPoster, &collHash, &fromTemplate); err != nil {
		t.Fatalf("read collection: %v", err)
	}
	if collPoster != "" || collHash != "" || fromTemplate {
		t.Fatalf("collection artwork not fully cleared: url=%q hash=%q from_template=%v", collPoster, collHash, fromTemplate)
	}

	var folderPoster string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_folders WHERE id = $1`, folderID).Scan(&folderPoster); err != nil {
		t.Fatalf("read folder: %v", err)
	}
	if folderPoster != "" {
		t.Fatalf("library poster not cleared: %q", folderPoster)
	}
}

func TestArtworkReconcileLeavesRowsAloneOnStorageErrors(t *testing.T) {
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
	contentID := fmt.Sprintf("arc-err-%d", suffix)
	okContentID := fmt.Sprintf("arc-err-ok-%d", suffix)
	cachedKey := fmt.Sprintf("tmdb/movies/%s/poster/original.webp", contentID)
	okKey := fmt.Sprintf("tmdb/movies/%s/poster/original.webp", okContentID)
	seed := func(id, key string) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
			VALUES ($1, 'movie', 'ARC Err', 'matched', '{}'::text[], $2, 'https://img.example/err.jpg')
		`, id, key); err != nil {
			t.Fatalf("seed item %s: %v", id, err)
		}
	}
	// The healthy sibling keeps the probe from concluding storage is
	// unreachable (an all-errored probe aborts before any sweep runs), so
	// the sweep-level skip-on-error behavior is what gets exercised.
	seed(contentID, cachedKey)
	seed(okContentID, okKey)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{contentID, okContentID})
	})

	checker := &fakeObjectChecker{erroring: map[string]bool{cachedKey: true}}
	stats, err := NewArtworkCacheReconciler(pool, checker).Run(ctx, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Errors == 0 {
		t.Fatal("expected the erroring key to be counted")
	}

	var posterPath string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, contentID).Scan(&posterPath); err != nil {
		t.Fatalf("read item: %v", err)
	}
	if posterPath != cachedKey {
		t.Fatalf("poster_path = %q, want untouched %q after storage error", posterPath, cachedKey)
	}
}

func TestArtworkReconcileAbortsWhenStorageUnreachable(t *testing.T) {
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
	contentID := fmt.Sprintf("arc-down-%d", suffix)
	cachedKey := fmt.Sprintf("tmdb/movies/%s/poster/original.webp", contentID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
		VALUES ($1, 'movie', 'ARC Down', 'matched', '{}'::text[], $2, 'https://img.example/down.jpg')
	`, contentID, cachedKey); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })

	// Every probe HEAD errors: storage is unreachable, which must abort the
	// run (missing ≠ unreachable) and leave the row untouched.
	checker := &fakeObjectChecker{errorAll: true}
	if _, err := NewArtworkCacheReconciler(pool, checker).Run(ctx, nil); err == nil {
		t.Fatal("Run with unreachable storage returned nil error")
	}

	var posterPath string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, contentID).Scan(&posterPath); err != nil {
		t.Fatalf("read item: %v", err)
	}
	if posterPath != cachedKey {
		t.Fatalf("poster_path = %q, want untouched %q after unreachable storage", posterPath, cachedKey)
	}
}

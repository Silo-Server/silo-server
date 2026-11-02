package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func TestRetryOnDeadlock(t *testing.T) {
	prevBackoff := artworkReconcileDeadlockBaseBackoff
	artworkReconcileDeadlockBaseBackoff = time.Millisecond
	t.Cleanup(func() { artworkReconcileDeadlockBaseBackoff = prevBackoff })

	deadlock := &pgconn.PgError{Code: "40P01"}
	ctx := context.Background()

	t.Run("retries deadlocks until success", func(t *testing.T) {
		calls := 0
		err := retryOnDeadlock(ctx, func() error {
			calls++
			if calls < 3 {
				return fmt.Errorf("exec: %w", deadlock)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("retryOnDeadlock: %v", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		calls := 0
		err := retryOnDeadlock(ctx, func() error {
			calls++
			return deadlock
		})
		if !errors.Is(err, deadlock) {
			t.Fatalf("err = %v, want the deadlock error", err)
		}
		if calls != artworkReconcileDeadlockMaxAttempts {
			t.Errorf("calls = %d, want %d", calls, artworkReconcileDeadlockMaxAttempts)
		}
	})

	t.Run("returns other errors immediately", func(t *testing.T) {
		calls := 0
		boom := errors.New("boom")
		if err := retryOnDeadlock(ctx, func() error {
			calls++
			return boom
		}); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("honors cancellation between attempts", func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		err := retryOnDeadlock(canceled, func() error { return deadlock })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

// presentObjects reports the keys it holds as present and any other as missing.
type presentObjects map[string]bool

func (p presentObjects) Stat(_ context.Context, key string) (blobstore.ObjectInfo, error) {
	if p[key] {
		return blobstore.ObjectInfo{}, nil
	}
	return blobstore.ObjectInfo{}, blobstore.ErrNotFound
}

// An uploaded image's row is the only pointer to its object: a bulk reset
// must keep the row while the object survives and clear it once it is gone.
func TestArtworkReconcileBulkResetVerifiesUploadsDB(t *testing.T) {
	pool := storedArtworkReleaseTestPool(t)
	ctx := t.Context()
	const (
		kept = "local/movies/up-kept/1a2b3c4d/poster/original.r1.webp"
		lost = "local/movies/up-lost/5e6f7a8b/poster/original.r1.webp"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, locked_fields, poster_path, poster_source_path) VALUES
		('provider', 'movie', '{}', 'tmdb/movies/1/poster/original.r1.webp', 'tmdb://poster/p.jpg'),
		('sidecar', 'movie', '{}', 'local/movies/2/poster/original.r1.webp', 'file:///m/poster.jpg?v=1-1'),
		('up-kept', 'movie', '{10}', $1, 'upload://sha256:aa'),
		('up-lost', 'movie', '{10}', $2, 'upload://sha256:bb')`, kept, lost); err != nil {
		t.Fatal(err)
	}
	var surface artworkSweepSurface
	for _, s := range artworkSweepSurfaces() {
		if s.name == "item posters" {
			surface = s
		}
	}

	r := &ArtworkCacheReconciler{pool: pool, s3: presentObjects{kept: true}}
	var stats ArtworkReconcileStats
	if err := r.bulkResetSurface(ctx, surface, &stats); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"provider": "tmdb://poster/p.jpg",
		"sidecar":  "",
		"up-kept":  kept,
		"up-lost":  "",
	}
	for id, path := range want {
		var got string
		if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != path {
			t.Errorf("%s poster_path = %q, want %q", id, got, path)
		}
	}
}

// Catalog reads trust the publication manifest, so the verify sweep checks every
// size a revision published, not only the stored key. A provider poster missing
// one size is queued for repair and keeps its pointer; an uploaded one keeps its
// row, since only its source could make the size again.
func TestArtworkReconcileFindsMissingPublishedSizeDB(t *testing.T) {
	pool := catalogCopyTestPool(t, "media_items", "media_item_localizations", "seasons", "season_localizations",
		"episodes", "people", "metadata_image_cache_jobs", "artwork_revision_gc_candidates")
	ctx := t.Context()
	const (
		provider = "tmdb/movies/9/poster/original.r1.webp"
		uploaded = "local/movies/up/1a2b3c4d/poster/original.r1.webp"
	)
	providerSize, uploadedSize := variantKey(provider, "w500"), variantKey(uploaded, "w500")
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, locked_fields, poster_path, poster_source_path) VALUES
		('provider', 'movie', 'Provider', '{}', $1, 'tmdb://poster/p.jpg'),
		('uploaded', 'movie', 'Uploaded', '{10}', $2, 'upload://sha256:aa')`, provider, uploaded); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates (original_path, image_type, object_keys, published_keys, not_before)
		VALUES ($1, 'poster', $2, $2, NOW()), ($3, 'poster', $4, $4, NOW())`,
		provider, []string{provider, providerSize}, uploaded, []string{uploaded, uploadedSize}); err != nil {
		t.Fatal(err)
	}
	var surface artworkSweepSurface
	for _, s := range artworkSweepSurfaces() {
		if s.name == "item posters" {
			surface = s
		}
	}
	// Both originals survive; both w500 sizes were deleted outside Silo.
	r := &ArtworkCacheReconciler{pool: pool, s3: presentObjects{provider: true, uploaded: true}}
	var stats ArtworkReconcileStats
	if err := r.sweepSurface(ctx, surface, &stats, func(int) {}); err != nil {
		t.Fatal(err)
	}
	if stats.Checked != 2 || stats.Repaired != 1 || stats.Requeued != 0 || stats.Cleared != 0 {
		t.Fatalf("stats = %#v, want the provider poster queued for repair and nothing reset", stats)
	}
	for id, path := range map[string]string{"provider": provider, "uploaded": uploaded} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != path {
			t.Errorf("%s poster_path = %q, want it kept as %q", id, got, path)
		}
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM metadata_image_cache_jobs WHERE target_content_id = 'provider'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("repair jobs for the provider poster = %d, want 1", jobs)
	}
}

// catalogCopyTestPool returns a pool on a new schema holding empty copies of the
// named public tables, so a sweep sees only the rows a test inserts.
func catalogCopyTestPool(t *testing.T, tables ...string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	schema := fmt.Sprintf("artwork_reconcile_test_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	for _, table := range tables {
		if _, err := admin.Exec(t.Context(), fmt.Sprintf("CREATE TABLE %s.%s (LIKE public.%s INCLUDING ALL)", schema, table, table)); err != nil {
			t.Fatal(err)
		}
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// A collection created from a built-in template can keep its bundled poster
// path, such as "/images/collection-templates/<id>.jpg". It is not a storage
// key: checking it failed every verify sweep with an invalid key, and a bulk
// reset cleared the poster. Both must leave the row alone.
func TestArtworkReconcileSkipsBundledPostersDB(t *testing.T) {
	pool := storedArtworkReleaseTestPool(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `CREATE TABLE library_collections (
		id TEXT PRIMARY KEY, poster_url TEXT DEFAULT '', poster_thumbhash TEXT DEFAULT '',
		poster_auto_generated BOOLEAN NOT NULL DEFAULT FALSE, poster_from_template BOOLEAN NOT NULL DEFAULT FALSE,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		t.Fatal(err)
	}
	const (
		bundled = "/images/collection-templates/arc.jpg"
		cached  = "collection-images/arc/poster.webp"
	)
	var surface artworkSweepSurface
	for _, s := range artworkSweepSurfaces() {
		if s.name == "collection posters" {
			surface = s
		}
	}
	r := &ArtworkCacheReconciler{pool: pool, s3: presentObjects{cached: true}}

	for _, tc := range []struct {
		name      string
		reconcile func(*ArtworkReconcileStats) error
	}{
		{"verify sweep", func(stats *ArtworkReconcileStats) error {
			return r.sweepSurface(ctx, surface, stats, func(int) {})
		}},
		{"bulk reset", func(stats *ArtworkReconcileStats) error {
			return r.bulkResetSurface(ctx, surface, stats)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE library_collections`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO library_collections (id, poster_url, poster_thumbhash, poster_from_template) VALUES
				('template', $1, 'aGFzaA==', TRUE), ('cached', $2, '', FALSE)`, bundled, cached); err != nil {
				t.Fatal(err)
			}
			var stats ArtworkReconcileStats
			if err := tc.reconcile(&stats); err != nil {
				t.Fatal(err)
			}
			if stats.Checked != 1 || stats.SweepErrors != 0 {
				t.Fatalf("stats = %#v, want only the cached poster checked and no sweep errors", stats)
			}
			var poster, hash string
			var fromTemplate bool
			if err := pool.QueryRow(ctx,
				`SELECT poster_url, poster_thumbhash, poster_from_template FROM library_collections WHERE id = 'template'`,
			).Scan(&poster, &hash, &fromTemplate); err != nil {
				t.Fatal(err)
			}
			if poster != bundled || hash != "aGFzaA==" || !fromTemplate {
				t.Fatalf("bundled poster changed: url=%q hash=%q from_template=%v", poster, hash, fromTemplate)
			}
		})
	}
}

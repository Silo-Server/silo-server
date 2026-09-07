package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

type deliveryTestChecker struct {
	existing    map[string]bool
	available   map[string]bool
	err         error
	beforeCheck func()
}

func (c *deliveryTestChecker) Bucket() string { return "test" }
func (c *deliveryTestChecker) ObjectExists(_ context.Context, _, key string) (bool, error) {
	if c.beforeCheck != nil {
		c.beforeCheck()
		c.beforeCheck = nil
	}
	if c.existing != nil {
		return c.existing[key], c.err
	}
	return true, c.err
}
func (c *deliveryTestChecker) ObjectAvailable(_ context.Context, _, key string) (bool, error) {
	return c.available[key], c.err
}

func TestArtworkDeliveryPublicationAndReconciliation(t *testing.T) {
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
	original := fmt.Sprintf("tmdb/movies/delivery-%d/poster/original.rev.webp", time.Now().UnixNano())
	large, medium := variantKey(original, "w780"), variantKey(original, "w500")
	keys := []string{original, large, medium}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path=$1`, original)
	})
	tracker := catalog.NewArtworkRevisionTracker(pool)
	store := NewArtworkDeliveryStore(pool, "delivery-a", true)
	read := func() ArtworkAvailability {
		t.Helper()
		states, err := store.ArtworkAvailability(ctx, []string{original})
		if err != nil {
			t.Fatal(err)
		}
		return states[original]
	}
	track := func(keys []string) {
		t.Helper()
		if err := tracker.TrackArtworkRevision(ctx, original, "poster", keys); err != nil {
			t.Fatal(err)
		}
	}
	// A legacy path displaced from one reference can still serve another.
	// GC registration alone is not evidence of an incomplete publication.
	if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates(original_path,image_type,not_before)
        VALUES($1,'poster',NOW())`, original); err != nil {
		t.Fatal(err)
	}
	legacyStates, err := store.ArtworkAvailability(ctx, []string{original})
	if err != nil {
		t.Fatal(err)
	}
	if _, known := legacyStates[original]; known {
		t.Fatal("legacy GC-only row became a known empty publication")
	}
	if got := selectPublishedVariant(large, legacyStates[original], false); got != medium {
		t.Fatalf("legacy fallback = %q", got)
	}
	track(nil)
	if got := selectPublishedVariant(large, read(), true); got != "" {
		t.Fatalf("partial publication advertised %q", got)
	}
	track(keys)
	if got := selectPublishedVariant(large, read(), true); got != medium {
		t.Fatalf("unverified delivery advertised %q", got)
	}
	// Beginning another upload does not discard previously published variants.
	track(nil)
	if !slices.Contains(read().Published, large) {
		t.Fatal("retry discarded publication")
	}
	checker := &deliveryTestChecker{available: map[string]bool{original: true, medium: true}}
	stats, err := store.Reconcile(ctx, checker)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Checked < 1 || stats.Missing < 1 {
		t.Fatalf("stats: %+v", stats)
	}
	if got := selectPublishedVariant(large, read(), true); got != medium {
		t.Fatalf("missing delivery advertised %q", got)
	}
	// A new scope must not inherit another endpoint's delivery verification.
	states, err := NewArtworkDeliveryStore(pool, "delivery-b", true).ArtworkAvailability(ctx, []string{original})
	if err != nil {
		t.Fatal(err)
	}
	if states[original].Verified {
		t.Fatal("verification crossed delivery configuration")
	}
	// Publication wakes verification; a delivery recovery restores the large URL.
	track(keys)
	checker.available[large] = true
	if _, err := store.Reconcile(ctx, checker); err != nil {
		t.Fatal(err)
	}
	if got := selectPublishedVariant(large, read(), true); got != large {
		t.Fatalf("recovery got %q", got)
	}
	// Transport errors retain the last completed verdict and schedule retry.
	track(keys)
	checker.err = errors.New("delivery unavailable")
	if _, err := store.Reconcile(ctx, checker); err != nil {
		t.Fatal(err)
	}
	if got := selectPublishedVariant(large, read(), true); got != large {
		t.Fatalf("outage erased last verdict: %q", got)
	}
	// A concurrent publication fences the in-flight verifier's stale result.
	track(keys)
	checker.err = nil
	checker.available = map[string]bool{}
	checker.beforeCheck = func() { track(keys) }
	if _, err := store.Reconcile(ctx, checker); err != nil {
		t.Fatal(err)
	}
	if got := selectPublishedVariant(large, read(), true); got != large {
		t.Fatalf("stale worker overwrote publication: %q", got)
	}
	if _, err := store.Reconcile(ctx, checker); err != nil {
		t.Fatal(err)
	}
	if got := selectPublishedVariant(large, read(), true); got != "" {
		t.Fatalf("known unavailable revision advertised %q", got)
	}
	// GC tombstones must never regain URLs via legacy fallback.
	if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates SET deleted_at=NOW() WHERE original_path=$1`, original); err != nil {
		t.Fatal(err)
	}
	if got := selectPublishedVariant(large, read(), true); got != "" {
		t.Fatalf("deleted revision advertised %q", got)
	}
	// Storage loss queues regeneration without changing catalog artwork pointers.
	track(keys)
	contentID := fmt.Sprintf("delivery-repair-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,genres,poster_path,poster_source_path,tmdb_id)
        VALUES($1,'movie','Delivery repair','{}',$2,'https://images.example/source.jpg','123')`, contentID, original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM metadata_image_cache_jobs WHERE target_content_id=$1`, contentID)
	})
	checker.existing = map[string]bool{original: true, medium: true}
	checker.available = map[string]bool{original: true, medium: true}
	if _, err := store.Reconcile(ctx, checker); err != nil {
		t.Fatal(err)
	}
	var savedPath, jobStatus string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id=$1`, contentID).Scan(&savedPath); err != nil {
		t.Fatal(err)
	}
	if savedPath != original {
		t.Fatal("repair replaced the catalog pointer")
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM metadata_image_cache_jobs WHERE target_content_id=$1`, contentID).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "queued" {
		t.Fatalf("repair status = %s", jobStatus)
	}

}

package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

type deliveryTestChecker struct {
	existing    map[string]bool
	available   map[string]bool
	err         error
	beforeCheck func() error
	mu          sync.Mutex
	hookErr     error
}

func (c *deliveryTestChecker) Bucket() string { return "test" }
func (c *deliveryTestChecker) ObjectExists(_ context.Context, _, key string) (bool, error) {
	c.mu.Lock()
	hook := c.beforeCheck
	c.beforeCheck = nil
	c.mu.Unlock()
	if hook != nil {
		if err := hook(); err != nil {
			c.mu.Lock()
			c.hookErr = err
			c.mu.Unlock()
			return false, err
		}
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
	checker.beforeCheck = func() error { return tracker.TrackArtworkRevision(ctx, original, "poster", keys) }
	if _, err := store.Reconcile(ctx, checker); err != nil {
		t.Fatal(err)
	}
	checker.mu.Lock()
	hookErr := checker.hookErr
	checker.mu.Unlock()
	if hookErr != nil {
		t.Fatal(hookErr)
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

func TestDeliveryCheckerConcurrentHook(t *testing.T) {
	var calls atomic.Int32
	hookFailure := errors.New("publication failed")
	checker := &deliveryTestChecker{beforeCheck: func() error {
		calls.Add(1)
		return hookFailure
	}}
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() { _, _ = checker.ObjectExists(t.Context(), "test", "key") })
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("hook called %d times", calls.Load())
	}
	if !errors.Is(checker.hookErr, hookFailure) {
		t.Fatalf("lost hook error: %v", checker.hookErr)
	}
}

func TestArtworkDeliveryVerifiesLegacyManifests(t *testing.T) {
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
	prefix := fmt.Sprintf("tmdb/movies/legacy-delivery-%d", time.Now().UnixNano())
	complete := prefix + "/poster/original.rev.webp"
	partial := prefix + "/backdrop/original.rev.webp"
	completeKeys := []string{complete, variantKey(complete, "w780")}
	partialKeys := []string{partial, variantKey(partial, "w1920")}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, []string{complete, partial})
	})
	for path, keys := range map[string][]string{complete: completeKeys, partial: partialKeys} {
		if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates(original_path,object_keys,not_before) VALUES($1,$2,NOW())`, path, keys); err != nil {
			t.Fatal(err)
		}
	}
	store := NewArtworkDeliveryStore(pool, "legacy-endpoint", true)
	states, err := store.ArtworkAvailability(ctx, []string{complete, partial})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatal("unverified legacy manifests became publication records")
	}
	checker := &deliveryTestChecker{
		existing:  map[string]bool{complete: true, completeKeys[1]: true, partial: true},
		available: map[string]bool{complete: true, completeKeys[1]: true, partial: true},
	}
	if _, err := store.Reconcile(ctx, checker); err != nil {
		t.Fatal(err)
	}
	states, err = store.ArtworkAvailability(ctx, []string{complete, partial})
	if err != nil {
		t.Fatal(err)
	}
	if !states[complete].Verified || len(states[complete].Published) != 2 {
		t.Fatal("storage-verified legacy artwork was not promoted")
	}
	if state := states[partial]; !state.Verified || len(state.Published) != 0 || selectPublishedVariant(partialKeys[1], state, true) != partial {
		t.Fatal("partial legacy delivery verdict did not select the surviving original")
	}
	checker.existing[partialKeys[1]] = true
	checker.available[partialKeys[1]] = true
	if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates SET delivery_next_check=NOW() WHERE original_path=$1`, partial); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reconcile(ctx, checker); err != nil {
		t.Fatal(err)
	}
	states, err = store.ArtworkAvailability(ctx, []string{partial})
	if err != nil {
		t.Fatal(err)
	}
	if !states[partial].Verified || len(states[partial].Published) != 2 {
		t.Fatal("repaired legacy artwork was not promoted")
	}
}

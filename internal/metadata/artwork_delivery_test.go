package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

type deliveryTestChecker struct {
	existing    map[string]bool
	available   map[string]bool
	err         error
	errKeys     map[string]bool
	beforeCheck func() error
	mu          sync.Mutex
	hookErr     error
}

func (c *deliveryTestChecker) Stat(_ context.Context, key string) (blobstore.ObjectInfo, error) {
	c.mu.Lock()
	hook := c.beforeCheck
	c.beforeCheck = nil
	c.mu.Unlock()
	if hook != nil {
		if err := hook(); err != nil {
			c.mu.Lock()
			c.hookErr = err
			c.mu.Unlock()
			return blobstore.ObjectInfo{}, err
		}
	}
	if c.err != nil {
		return blobstore.ObjectInfo{}, c.err
	}
	if c.errKeys[key] {
		return blobstore.ObjectInfo{}, errors.New("storage probe timed out")
	}
	if c.existing != nil && !c.existing[key] {
		return blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	return blobstore.ObjectInfo{Key: key}, nil
}
func (c *deliveryTestChecker) ObjectAvailable(_ context.Context, key string) (bool, error) {
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

func artworkDeliveryTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Production stalled here: an unavailable verdict recorded just before a
// successful re-upload kept suppressing the artwork, and the queued recheck sat
// behind every overdue routine check. A re-upload must restore a URL without a
// probe, and the verifier must confirm it ahead of a backlog larger than a batch.
func TestArtworkDeliveryRecoversRepublishedArtworkBehindBacklog(t *testing.T) {
	pool := artworkDeliveryTestPool(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("tmdb/movies/delivery-backlog-%d", time.Now().UnixNano())
	original := prefix + "/target/poster/original.rev.webp"
	large, medium := variantKey(original, "w780"), variantKey(original, "w500")
	keys := []string{original, large, medium}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, prefix+"/%")
	})
	const backlog = 3*artworkDeliveryBatchSize + 50
	if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates(
            original_path, image_type, object_keys, published_keys, delivery_keys, delivery_scope,
            delivery_checked_at, delivery_next_check, not_before)
        SELECT p, 'poster', ARRAY[p], ARRAY[p], ARRAY[p], 'delivery-backlog', NOW() - INTERVAL '30 days', '-infinity', NOW()
        FROM (SELECT $1 || '/backlog-' || n || '/poster/original.rev.webp' AS p FROM generate_series(1, $2) n) rows`,
		prefix, backlog); err != nil {
		t.Fatal(err)
	}
	dueBacklog := func() int {
		t.Helper()
		var due int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM artwork_revision_gc_candidates
            WHERE original_path LIKE $1 AND delivery_next_check <= NOW()`, prefix+"/backlog-%").Scan(&due); err != nil {
			t.Fatal(err)
		}
		return due
	}

	store := NewArtworkDeliveryStore(pool, "delivery-backlog", true)
	read := func(store *ArtworkDeliveryStore) ArtworkAvailability {
		t.Helper()
		states, err := store.ArtworkAvailability(ctx, []string{original})
		if err != nil {
			t.Fatal(err)
		}
		return states[original]
	}
	tracker := catalog.NewArtworkRevisionTracker(pool)
	if err := tracker.TrackArtworkRevision(ctx, original, "poster", keys); err != nil {
		t.Fatal(err)
	}
	// The verifier found nothing deliverable a week ago, then the repair ran.
	if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates
        SET delivery_keys = '{}', delivery_scope = 'delivery-backlog', delivery_checked_at = NOW() - INTERVAL '8 days',
            delivery_next_check = NOW() + INTERVAL '1 hour'
        WHERE original_path = $1`, original); err != nil {
		t.Fatal(err)
	}
	if got := selectPublishedVariant(large, read(store), true); got != "" {
		t.Fatalf("known unavailable revision advertised %q", got)
	}
	if err := tracker.TrackArtworkRevision(ctx, original, "poster", nil); err != nil {
		t.Fatal(err)
	}
	if got := selectPublishedVariant(large, read(store), true); got != "" {
		t.Fatalf("an upload that has not finished cleared the verdict: %q", got)
	}
	if err := tracker.TrackArtworkRevision(ctx, original, "poster", keys); err != nil {
		t.Fatal(err)
	}
	if got := selectPublishedVariant(large, read(store), true); got != medium {
		t.Fatalf("re-uploaded artwork resolved to %q, want the established rung %q", got, medium)
	}

	checker := &deliveryTestChecker{available: map[string]bool{original: true, large: true, medium: true}}
	stats, err := store.reconcileBatch(ctx, checker)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending < 1 || stats.Checked != artworkDeliveryBatchSize {
		t.Fatalf("batch stats: %+v", stats)
	}
	if state := read(store); !state.Verified || selectPublishedVariant(large, state, true) != large {
		t.Fatalf("republished artwork was not verified in the first batch: %+v", state)
	}
	if due := dueBacklog(); due < backlog-artworkDeliveryBatchSize+1 {
		t.Fatalf("only %d of %d backlog checks remain; the target did not jump the queue", due, backlog)
	}

	// A probe outage ends the run after one batch and leaves the remaining
	// backlog and the failure in the run's stats.
	checker.err = errors.New("delivery probe returned status 503")
	stats, err = store.Reconcile(ctx, checker)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Errors < 1 || stats.Checked > artworkDeliveryBatchSize || !strings.Contains(stats.LastError, "status 503") {
		t.Fatalf("probe failure stats: %+v", stats)
	}
	if stats.Overdue < int64(dueBacklog()) || dueBacklog() < backlog-2*artworkDeliveryBatchSize {
		t.Fatalf("overdue = %d with %d backlog checks due", stats.Overdue, dueBacklog())
	}
}

// A verdict from before a re-upload still proves which variants delivered, so
// the widest rung stays available while the recheck is pending. It proves
// nothing for another delivery configuration.
func TestArtworkDeliveryStaleVerdictKeepsSurvivingVariants(t *testing.T) {
	pool := artworkDeliveryTestPool(t)
	ctx := t.Context()
	original := fmt.Sprintf("tmdb/movies/delivery-stale-%d/poster/original.rev.webp", time.Now().UnixNano())
	large, medium := variantKey(original, "w780"), variantKey(original, "w500")
	keys := []string{original, large, medium}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path=$1`, original)
	})
	tracker := catalog.NewArtworkRevisionTracker(pool)
	if err := tracker.TrackArtworkRevision(ctx, original, "poster", keys); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates
        SET delivery_keys = $2, delivery_scope = 'delivery-stale', delivery_checked_at = NOW()
        WHERE original_path = $1`, original, keys); err != nil {
		t.Fatal(err)
	}
	if err := tracker.TrackArtworkRevision(ctx, original, "poster", keys); err != nil {
		t.Fatal(err)
	}
	for scope, want := range map[string]string{"delivery-stale": large, "delivery-other": medium} {
		states, err := NewArtworkDeliveryStore(pool, scope, true).ArtworkAvailability(ctx, []string{original})
		if err != nil {
			t.Fatal(err)
		}
		state := states[original]
		if state.Verified {
			t.Fatalf("%s: verdict survived publication", scope)
		}
		if got := selectPublishedVariant(large, state, true); got != want {
			t.Fatalf("%s: resolved %q, want %q", scope, got, want)
		}
	}
}

func TestArtworkDeliveryBacksOffIncompleteVerdicts(t *testing.T) {
	pool := artworkDeliveryTestPool(t)
	ctx := t.Context()
	original := fmt.Sprintf("tmdb/movies/delivery-backoff-%d/poster/original.rev.webp", time.Now().UnixNano())
	large, medium := variantKey(original, "w780"), variantKey(original, "w500")
	keys := []string{original, large, medium}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path=$1`, original)
	})
	tracker := catalog.NewArtworkRevisionTracker(pool)
	if err := tracker.TrackArtworkRevision(ctx, original, "poster", keys); err != nil {
		t.Fatal(err)
	}
	store := NewArtworkDeliveryStore(pool, "delivery-backoff", true)
	checker := &deliveryTestChecker{available: map[string]bool{original: true, medium: true}}
	check := func(wantFailures int, wantDelay time.Duration) {
		t.Helper()
		if _, err := store.reconcileBatch(ctx, checker); err != nil {
			t.Fatal(err)
		}
		var failures int
		var delay float64
		if err := pool.QueryRow(ctx, `SELECT delivery_failures, extract(epoch FROM delivery_next_check - NOW())
            FROM artwork_revision_gc_candidates WHERE original_path = $1`, original).Scan(&failures, &delay); err != nil {
			t.Fatal(err)
		}
		if failures != wantFailures || time.Duration(delay*float64(time.Second)) > wantDelay ||
			time.Duration(delay*float64(time.Second)) < wantDelay-time.Minute {
			t.Fatalf("failures=%d next check in %.0fs, want %d and %s", failures, delay, wantFailures, wantDelay)
		}
		// Make the routine recheck due ahead of anything else in the database.
		if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates SET delivery_next_check = '-infinity'
            WHERE original_path = $1`, original); err != nil {
			t.Fatal(err)
		}
	}
	check(1, 15*time.Minute)
	check(2, 30*time.Minute)
	checker.available[large] = true
	check(0, 7*24*time.Hour)
	delete(checker.available, large)
	check(1, 15*time.Minute)
	// A probe error backs off on the same schedule and keeps the verdict.
	checker.err = errors.New("delivery probe returned status 403")
	check(2, 30*time.Minute)
	checker.err = nil
	states, err := store.ArtworkAvailability(ctx, []string{original})
	if err != nil {
		t.Fatal(err)
	}
	if state := states[original]; !state.Verified || len(state.Deliverable) != 2 {
		t.Fatalf("probe error replaced the verdict: %+v", state)
	}
	if err := tracker.TrackArtworkRevision(ctx, original, "poster", keys); err != nil {
		t.Fatal(err)
	}
	var failures int
	var checked *time.Time
	if err := pool.QueryRow(ctx, `SELECT delivery_failures, delivery_checked_at FROM artwork_revision_gc_candidates
        WHERE original_path = $1`, original).Scan(&failures, &checked); err != nil {
		t.Fatal(err)
	}
	if failures != 0 || checked != nil {
		t.Fatalf("publication kept failures=%d checked=%v", failures, checked)
	}
}

func TestArtworkDeliveryRecheckAfter(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		0: 7 * 24 * time.Hour, 1: 15 * time.Minute, 2: 30 * time.Minute, 3: time.Hour,
		7: 16 * time.Hour, 8: 24 * time.Hour, 1000: 24 * time.Hour,
	} {
		if got := artworkDeliveryRecheckAfter(failures); got != want {
			t.Errorf("artworkDeliveryRecheckAfter(%d) = %s, want %s", failures, got, want)
		}
	}
}

// One failing probe must not end the run: a revision that always errors would
// otherwise cut every run back to a single batch.
func TestArtworkDeliveryContinuesPastIsolatedProbeErrors(t *testing.T) {
	pool := artworkDeliveryTestPool(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("tmdb/movies/delivery-isolated-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, prefix+"/%")
	})
	const rows = 2*artworkDeliveryBatchSize + 50
	if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates(
            original_path, image_type, object_keys, published_keys, delivery_keys, delivery_scope,
            delivery_checked_at, delivery_next_check, not_before)
        SELECT p, 'poster', ARRAY[p], ARRAY[p], ARRAY[p], 'delivery-isolated', NOW() - INTERVAL '30 days', '-infinity', NOW()
        FROM (SELECT $1 || '/' || n || '/poster/original.rev.webp' AS p FROM generate_series(1, $2) n) paths`,
		prefix, rows); err != nil {
		t.Fatal(err)
	}
	failing := prefix + "/1/poster/original.rev.webp"
	checker := &deliveryTestChecker{available: map[string]bool{}, errKeys: map[string]bool{failing: true}}
	for n := 1; n <= rows; n++ {
		checker.available[fmt.Sprintf("%s/%d/poster/original.rev.webp", prefix, n)] = true
	}
	stats, err := NewArtworkDeliveryStore(pool, "delivery-isolated", true).Reconcile(ctx, checker)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Errors < 1 || stats.Checked < rows {
		t.Fatalf("run stopped at the failing probe: %+v", stats)
	}
	var due, failures int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE delivery_next_check <= NOW()),
            max(delivery_failures) FILTER (WHERE original_path = $2)
        FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, prefix+"/%", failing).Scan(&due, &failures); err != nil {
		t.Fatal(err)
	}
	if due != 0 || failures != 1 {
		t.Fatalf("%d checks still due, failing revision failures=%d", due, failures)
	}
}

// A verdict from another delivery configuration reads as unverified, so the
// verifier must recheck it promptly rather than at its old recheck time.
func TestArtworkDeliveryRequeuesVerdictsFromAnotherScope(t *testing.T) {
	pool := artworkDeliveryTestPool(t)
	ctx := t.Context()
	original := fmt.Sprintf("tmdb/movies/delivery-rescope-%d/poster/original.rev.webp", time.Now().UnixNano())
	keys := []string{original, variantKey(original, "w500")}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path=$1`, original)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates(
            original_path, image_type, object_keys, published_keys, delivery_keys, delivery_scope,
            delivery_checked_at, delivery_next_check, not_before)
        VALUES ($1, 'poster', $2, $2, $2, 'delivery-old', NOW(), NOW() + INTERVAL '7 days', NOW())`, original, keys); err != nil {
		t.Fatal(err)
	}
	store := NewArtworkDeliveryStore(pool, "delivery-new", true)
	checker := &deliveryTestChecker{available: map[string]bool{keys[0]: true, keys[1]: true}}
	stats, err := store.Reconcile(ctx, checker)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rescoped < 1 {
		t.Fatalf("stats: %+v", stats)
	}
	states, err := store.ArtworkAvailability(ctx, []string{original})
	if err != nil {
		t.Fatal(err)
	}
	if state := states[original]; !state.Verified || len(state.Deliverable) != 2 {
		t.Fatalf("verdict from another scope was not rechecked: %+v", state)
	}
	// A replica still on the old configuration records another old-scope
	// verdict after the sweep. The next sweep, an interval later, finds it.
	if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates
        SET delivery_scope = 'delivery-old', delivery_next_check = NOW() + INTERVAL '7 days'
        WHERE original_path = $1`, original); err != nil {
		t.Fatal(err)
	}
	if stats, err = store.Reconcile(ctx, checker); err != nil || stats.Rescoped != 0 {
		t.Fatalf("sweep repeated within its interval: rescoped %d: %v", stats.Rescoped, err)
	}
	store.scopeSweptAt.Store(time.Now().Add(-artworkDeliveryRescopeInterval).UnixNano())
	if stats, err = store.Reconcile(ctx, checker); err != nil || stats.Rescoped < 1 {
		t.Fatalf("later sweep missed an old-scope verdict: rescoped %d: %v", stats.Rescoped, err)
	}
	states, err = store.ArtworkAvailability(ctx, []string{original})
	if err != nil {
		t.Fatal(err)
	}
	if state := states[original]; !state.Verified {
		t.Fatalf("old-scope verdict was not rechecked: %+v", state)
	}
}

func TestArtworkDeliveryErrorTextRedactsURLQueries(t *testing.T) {
	err := fmt.Errorf("probe: %w", errors.New(`Get "https://cdn.example/poster/w500.rev.webp?X-Amz-Signature=secret&token=abc": dial tcp: timeout`))
	got := artworkDeliveryErrorText(err)
	if strings.Contains(got, "secret") || strings.Contains(got, "token=") || !strings.Contains(got, "https://cdn.example/poster/w500.rev.webp?[redacted]") {
		t.Fatalf("error text = %q", got)
	}
	if got := artworkDeliveryErrorText(errors.New(strings.Repeat("é", 400))); len(got) > 500 || !utf8.ValidString(got) {
		t.Fatalf("long error text was not capped cleanly: %d bytes", len(got))
	}
}

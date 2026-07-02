package catalog

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCoverageModels is an in-memory CatalogSemanticModelProvider. It returns a
// queued sequence of (model, err) results so a test can flip the active model
// between Refresh calls without touching a real recommendations engine.
type fakeCoverageModels struct {
	mu      sync.Mutex
	results []struct {
		model string
		err   error
	}
	idx int
}

func (f *fakeCoverageModels) push(model string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, struct {
		model string
		err   error
	}{model, err})
}

func (f *fakeCoverageModels) ActiveEmbeddingModel(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		return "", nil
	}
	if f.idx >= len(f.results) {
		// Repeat the last queued result for steady-state callers (e.g. the
		// -race loops) so they keep observing a stable model.
		last := f.results[len(f.results)-1]
		return last.model, last.err
	}
	r := f.results[f.idx]
	f.idx++
	return r.model, r.err
}

// constCoverageModels always returns the same model with no error.
type constCoverageModels struct{ model string }

func (c constCoverageModels) ActiveEmbeddingModel(_ context.Context) (string, error) {
	return c.model, nil
}

// fatalFetch is a fetch seam that fails the test if it is ever invoked. It
// proves the read path never reaches the database.
func fatalFetch(t *testing.T) func(context.Context, string) ([]catalogTypeCoverage, error) {
	t.Helper()
	return func(context.Context, string) ([]catalogTypeCoverage, error) {
		t.Fatal("fetch must not be called")
		return nil, nil
	}
}

// TestCoverageReadyNilSnapshotIsSafe verifies the fail-safe contract: a
// freshly-constructed tracker that has never refreshed reports not-ready, never
// panics, and never touches the fetch seam (read path is lock/DB free).
func TestCoverageReadyNilSnapshotIsSafe(t *testing.T) {
	tr := &semanticCoverageTracker{fetch: fatalFetch(t), clock: time.Now}

	ready, reason := tr.CoverageReady(nil)
	if ready {
		t.Fatalf("nil-snapshot tracker reported ready")
	}
	if reason != "coverage not yet computed" {
		t.Fatalf("reason = %q, want %q", reason, "coverage not yet computed")
	}
	if tr.Snapshot() != nil {
		t.Fatalf("Snapshot() = %+v, want nil before any refresh", tr.Snapshot())
	}
}

// TestCoverageRefreshEmptyModelDoesNotFetch verifies that when no embedding
// model is active (no lock / no provider value) Refresh publishes an empty
// not-ready snapshot without ever consulting the fetch seam.
func TestCoverageRefreshEmptyModelDoesNotFetch(t *testing.T) {
	models := &fakeCoverageModels{}
	models.push("", nil)
	tr := &semanticCoverageTracker{fetch: fatalFetch(t), models: models, clock: time.Now}

	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	ready, reason := tr.CoverageReady(nil)
	if ready {
		t.Fatalf("empty-model tracker reported ready (reason %q)", reason)
	}
	snap := tr.Snapshot()
	if snap == nil {
		t.Fatalf("Snapshot() = nil after Refresh, want empty snapshot")
	}
	if snap.Model != "" {
		t.Fatalf("snapshot model = %q, want empty", snap.Model)
	}
}

// TestCoverageRefreshModelErrorRetainsLastGood verifies that a provider error
// is fail-safe: the previous good snapshot is retained, not overwritten with a
// not-ready one.
func TestCoverageRefreshModelErrorRetainsLastGood(t *testing.T) {
	models := &fakeCoverageModels{}
	models.push("model-a", nil)
	models.push("", errors.New("lock unavailable"))

	calls := 0
	tr := &semanticCoverageTracker{
		fetch: func(_ context.Context, model string) ([]catalogTypeCoverage, error) {
			calls++
			return []catalogTypeCoverage{{Type: "movie", Eligible: 10, Vectorized: 10}}, nil
		},
		models: models,
		clock:  time.Now,
	}

	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh returned error: %v", err)
	}
	good := tr.Snapshot()
	if good == nil || good.Model != "model-a" {
		t.Fatalf("first snapshot = %+v, want model-a", good)
	}

	if err := tr.Refresh(context.Background()); err == nil {
		t.Fatalf("second Refresh should surface provider error")
	}
	after := tr.Snapshot()
	if after != good {
		t.Fatalf("provider error replaced snapshot: before=%p after=%p", good, after)
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times, want 1 (provider error short-circuits)", calls)
	}
}

// TestComputeCoverageSnapshotHysteresis exercises the pure hysteresis helper
// across the enable/disable band: 0.85 (nil prev) -> not ready, 0.92 -> ready,
// 0.85 (prev ready) -> stays ready (latched), 0.79 -> not ready.
func TestComputeCoverageSnapshotHysteresis(t *testing.T) {
	now := time.Unix(0, 0)
	step := func(prev *semanticCoverageSnapshot, vectorized int) *semanticCoverageSnapshot {
		return computeCoverageSnapshot(
			[]catalogTypeCoverage{{Type: "movie", Eligible: 100, Vectorized: vectorized}},
			"m", prev, now,
		)
	}

	s1 := step(nil, 85) // 0.85, band, nil prev -> not ready
	if s1.PerType["movie"].Ready {
		t.Fatalf("0.85 with nil prev should not be ready")
	}

	s2 := step(s1, 92) // 0.92 >= enable -> ready
	if !s2.PerType["movie"].Ready {
		t.Fatalf("0.92 should be ready")
	}

	s3 := step(s2, 85) // 0.85, band, prev ready -> stays ready
	if !s3.PerType["movie"].Ready {
		t.Fatalf("0.85 with ready prev should latch ready")
	}

	s4 := step(s3, 79) // 0.79 < disable -> not ready
	if s4.PerType["movie"].Ready {
		t.Fatalf("0.79 should drop ready below disable threshold")
	}

	// Sanity check the recorded ratio on a band entry.
	if got := s3.PerType["movie"].Ratio; got < 0.849 || got > 0.851 {
		t.Fatalf("ratio = %v, want ~0.85", got)
	}
}

// TestComputeCoverageSnapshotZeroEligibleNotReady verifies a type with no
// eligible items is never marked ready and contributes nothing to overall.
func TestComputeCoverageSnapshotZeroEligibleNotReady(t *testing.T) {
	s := computeCoverageSnapshot(
		[]catalogTypeCoverage{{Type: "movie", Eligible: 0, Vectorized: 0}},
		"m", nil, time.Unix(0, 0),
	)
	c := s.PerType["movie"]
	if c.Ready {
		t.Fatalf("zero-eligible type should not be ready")
	}
	if c.Ratio != 0 {
		t.Fatalf("zero-eligible ratio = %v, want 0", c.Ratio)
	}
	if s.Overall != 0 {
		t.Fatalf("overall = %v, want 0 when nothing eligible", s.Overall)
	}
}

// TestCoverageReadyScopeAND verifies scope semantics: an explicit scope is an
// AND over requested types, the reason names the first failing type, and an
// empty scope requires every snapshot type to be ready.
func TestCoverageReadyScopeAND(t *testing.T) {
	now := time.Unix(0, 0)
	snap := computeCoverageSnapshot(
		[]catalogTypeCoverage{
			{Type: "movie", Eligible: 100, Vectorized: 95},  // ready
			{Type: "series", Eligible: 100, Vectorized: 50}, // not ready
		},
		"m", nil, now,
	)
	tr := &semanticCoverageTracker{fetch: fatalFetch(t), clock: time.Now}
	tr.snap.Store(snap)

	if ready, reason := tr.CoverageReady([]string{"movie", "series"}); ready {
		t.Fatalf("scope with not-ready series reported ready")
	} else if !strings.Contains(reason, "series") {
		t.Fatalf("reason = %q, should name the failing type series", reason)
	}

	if ready, _ := tr.CoverageReady([]string{"movie"}); !ready {
		t.Fatalf("movie-only scope should be ready")
	}

	// Empty scope = all snapshot types; series is not ready so overall fails.
	if ready, reason := tr.CoverageReady(nil); ready {
		t.Fatalf("empty scope should require all types ready, reason=%q", reason)
	}
}

// TestCoverageReadyScopeUnknownTypeNotGated verifies that a requested type with
// no snapshot row (no eligible items) is skipped, and a scope consisting only
// of such types reports the no-embeddable-items reason rather than panicking.
func TestCoverageReadyScopeUnknownTypeNotGated(t *testing.T) {
	snap := computeCoverageSnapshot(
		[]catalogTypeCoverage{{Type: "movie", Eligible: 100, Vectorized: 95}},
		"m", nil, time.Unix(0, 0),
	)
	tr := &semanticCoverageTracker{fetch: fatalFetch(t), clock: time.Now}
	tr.snap.Store(snap)

	// movie present+ready, "music" absent -> gated only on the present type.
	if ready, _ := tr.CoverageReady([]string{"movie", "music"}); !ready {
		t.Fatalf("absent type should not gate a ready present type")
	}

	// Scope of only-absent types -> nothing to gate -> not ready with reason.
	if ready, reason := tr.CoverageReady([]string{"music"}); ready {
		t.Fatalf("scope with no embeddable items reported ready")
	} else if !strings.Contains(reason, "no embeddable items") {
		t.Fatalf("reason = %q, want no-embeddable-items message", reason)
	}
}

// TestCoverageRefreshModelCollapse verifies that when the active embedding model
// changes the next Refresh immediately drops the prior latches: the published
// snapshot carries the new model and is not-ready before recompute uses stale
// hysteresis state.
func TestCoverageRefreshModelCollapse(t *testing.T) {
	models := &fakeCoverageModels{}
	models.push("model-a", nil)
	models.push("model-b", nil)

	// fetch always reports full coverage; only the model identity changes.
	tr := &semanticCoverageTracker{
		fetch: func(_ context.Context, model string) ([]catalogTypeCoverage, error) {
			return []catalogTypeCoverage{{Type: "movie", Eligible: 100, Vectorized: 100}}, nil
		},
		models: models,
		clock:  time.Now,
	}

	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(A) error: %v", err)
	}
	a := tr.Snapshot()
	if a.Model != "model-a" || !a.PerType["movie"].Ready {
		t.Fatalf("after Refresh(A): model=%q ready=%v, want model-a ready", a.Model, a.PerType["movie"].Ready)
	}

	// Collapse: the latch must drop the instant the model changes. We assert on
	// the recomputed snapshot which now reports model-b. The prev passed into
	// compute is the collapsed (empty) snapshot, so the band latch can never
	// carry model-a readiness into model-b.
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(B) error: %v", err)
	}
	b := tr.Snapshot()
	if b.Model != "model-b" {
		t.Fatalf("after Refresh(B): model=%q, want model-b", b.Model)
	}
}

// TestCoverageRefreshBandCollapseDropsLatch is the sharper collapse assertion:
// it drives a band ratio (0.85) under the new model so readiness can only come
// from a latch. Because the collapse zeroes prev, the new-model snapshot must be
// not-ready even though the same type was ready under the old model.
func TestCoverageRefreshBandCollapseDropsLatch(t *testing.T) {
	models := &fakeCoverageModels{}
	models.push("model-a", nil) // full coverage -> ready latch under A
	models.push("model-b", nil) // band coverage under B

	var vectorized atomic.Int64
	vectorized.Store(100)
	tr := &semanticCoverageTracker{
		fetch: func(_ context.Context, model string) ([]catalogTypeCoverage, error) {
			return []catalogTypeCoverage{{
				Type: "movie", Eligible: 100, Vectorized: int(vectorized.Load()),
			}}, nil
		},
		models: models,
		clock:  time.Now,
	}

	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(A) error: %v", err)
	}
	if !tr.Snapshot().PerType["movie"].Ready {
		t.Fatalf("movie should be ready under model-a at full coverage")
	}

	vectorized.Store(85) // band [0.80,0.90) under model-b
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(B) error: %v", err)
	}
	b := tr.Snapshot()
	if b.Model != "model-b" {
		t.Fatalf("snapshot model = %q, want model-b", b.Model)
	}
	if b.PerType["movie"].Ready {
		t.Fatalf("band coverage under a new model must not inherit the old-model latch")
	}
}

// TestCoverageConcurrentRefreshAndRead is the -race gate: many goroutines call
// Refresh while many call CoverageReady. UpdatedAt must advance monotonically
// (single-flight + immutable publish) and the race detector must stay silent.
func TestCoverageConcurrentRefreshAndRead(t *testing.T) {
	// Monotonic clock so successive successful publishes carry strictly
	// increasing UpdatedAt regardless of wall-clock resolution.
	var ticks atomic.Int64
	clock := func() time.Time { return time.Unix(0, ticks.Add(1)) }

	tr := &semanticCoverageTracker{
		fetch: func(_ context.Context, model string) ([]catalogTypeCoverage, error) {
			return []catalogTypeCoverage{
				{Type: "movie", Eligible: 100, Vectorized: 95},
				{Type: "series", Eligible: 100, Vectorized: 85},
			}, nil
		},
		models: constCoverageModels{model: "m"},
		clock:  clock,
	}

	ctx := context.Background()
	var wg sync.WaitGroup

	// Writers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if err := tr.Refresh(ctx); err != nil {
					t.Errorf("Refresh error: %v", err)
					return
				}
			}
		}()
	}

	// Readers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last time.Time
			for j := 0; j < 500; j++ {
				_, _ = tr.CoverageReady([]string{"movie"})
				if s := tr.Snapshot(); s != nil {
					if s.UpdatedAt.Before(last) {
						t.Errorf("UpdatedAt went backwards: %v < %v", s.UpdatedAt, last)
						return
					}
					last = s.UpdatedAt
					// Touch the published map to prove it is never mutated
					// after Store (would trip the race detector otherwise).
					_ = s.PerType["movie"].Ready
				}
			}
		}()
	}

	wg.Wait()
}

// TestCoverageRunStopsOnContextCancel verifies Run performs an immediate refresh
// and returns promptly when the context is canceled (ticker lifecycle).
func TestCoverageRunStopsOnContextCancel(t *testing.T) {
	var refreshes atomic.Int64
	tr := &semanticCoverageTracker{
		fetch: func(_ context.Context, model string) ([]catalogTypeCoverage, error) {
			refreshes.Add(1)
			return []catalogTypeCoverage{{Type: "movie", Eligible: 1, Vectorized: 1}}, nil
		},
		models: constCoverageModels{model: "m"},
		clock:  time.Now,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
	if refreshes.Load() == 0 {
		t.Fatalf("Run should perform an immediate refresh before ticking")
	}
}

// TestNewSemanticCoverageTrackerCoverageDBSmoke is the single DB-backed test. It
// proves the default fetch wiring reaches catalogSemanticCoverageByType through
// a real pool. Skipped unless SILO_TEST_DATABASE_URL is set.
func TestNewSemanticCoverageTrackerCoverageDBSmoke(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	const prefix = "covtrack-smoke-"
	cleanupSemanticCoverageItems(t, pool, prefix)

	seedSemanticCoverageMediaItem(t, pool, prefix+"m1", "movie", "matched")
	seedSemanticCoverageMediaItem(t, pool, prefix+"m2", "movie", "matched")
	seedSemanticCoverageEmbedding(t, pool, prefix+"m1", "smoke-model")

	tr := newSemanticCoverageTracker(pool, []string{"movie"}, constCoverageModels{model: "smoke-model"})
	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh against real pool: %v", err)
	}
	snap := tr.Snapshot()
	if snap == nil {
		t.Fatalf("Snapshot() = nil after DB-backed refresh")
	}
	cov := snap.PerType["movie"]
	if cov.Eligible < 2 {
		t.Fatalf("movie eligible = %d, want >= 2 seeded rows", cov.Eligible)
	}
	if cov.Vectorized < 1 {
		t.Fatalf("movie vectorized = %d, want >= 1 seeded embedding", cov.Vectorized)
	}
	if cov.Vectorized > cov.Eligible {
		t.Fatalf("vectorized %d exceeds eligible %d", cov.Vectorized, cov.Eligible)
	}
}

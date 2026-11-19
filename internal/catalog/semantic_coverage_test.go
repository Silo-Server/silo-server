package catalog

import (
	"context"
	"errors"

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

// TestCoverageRefreshModelCollapse verifies that when the active embedding model
// changes the next Refresh publishes a snapshot for the new model.
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

	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(B) error: %v", err)
	}
	b := tr.Snapshot()
	if b.Model != "model-b" {
		t.Fatalf("after Refresh(B): model=%q, want model-b", b.Model)
	}
}

// TestCoverageRefreshModelChangeWithFailedRecount verifies that a model change
// whose recount fails leaves a not-ready snapshot for the new model, never the
// previous model's readiness.
func TestCoverageRefreshModelChangeWithFailedRecount(t *testing.T) {
	models := &fakeCoverageModels{}
	models.push("model-a", nil)
	models.push("model-b", nil)

	var fail atomic.Bool
	tr := &semanticCoverageTracker{
		fetch: func(_ context.Context, model string) ([]catalogTypeCoverage, error) {
			if fail.Load() {
				return nil, errors.New("count query failed")
			}
			return []catalogTypeCoverage{{Type: "movie", Eligible: 100, Vectorized: 100}}, nil
		},
		models: models,
		clock:  time.Now,
	}

	if err := tr.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(A) error: %v", err)
	}
	if ready, _ := tr.CoverageReady(nil); !ready {
		t.Fatalf("movie should be ready under model-a at full coverage")
	}

	fail.Store(true)
	if err := tr.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh(B) should surface the count failure")
	}
	b := tr.Snapshot()
	if b.Model != "model-b" {
		t.Fatalf("snapshot model = %q, want model-b", b.Model)
	}
	if ready, _ := tr.CoverageReady(nil); ready {
		t.Fatalf("a failed recount under a new model must not keep the old model's readiness")
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

package worker

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fakePersonRefresher hands out claimed batches in order and records which
// people were refreshed.
type fakePersonRefresher struct {
	mu        sync.Mutex
	batches   [][]int64
	claims    int
	refreshed []int64
	onRefresh func(id int64)
	errs      map[int64]error
}

func (f *fakePersonRefresher) ClaimCandidates(_ context.Context, _ int) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	if len(f.batches) == 0 {
		return nil, nil
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return batch, nil
}

func (f *fakePersonRefresher) RefreshPerson(_ context.Context, id int64) (*models.Person, error) {
	f.mu.Lock()
	f.refreshed = append(f.refreshed, id)
	hook := f.onRefresh
	f.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	return &models.Person{ID: id}, nil
}

func newTestPersonRefreshWorker(service PersonRefresher) *PersonRefreshWorker {
	return NewPersonRefreshWorker(service, PersonRefreshWorkerConfig{BatchSize: 3})
}

// A backlog is worked through batch after batch, not one batch per interval.
func TestPersonRefreshWorkerDrainsFullBatchesBackToBack(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2, 3}, {4, 5, 6}, {7}}}
	w := newTestPersonRefreshWorker(service)

	w.drain()

	if want := []int64{1, 2, 3, 4, 5, 6, 7}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}
	// Two full batches, then the short one ends the drain without a fourth claim.
	if service.claims != 3 {
		t.Fatalf("claims = %d, want 3", service.claims)
	}
}

// A person-page request that arrives during a batch runs before the rest of it.
func TestPersonRefreshWorkerRunsOnDemandRequestsBetweenBatchItems(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2, 3}}}
	w := newTestPersonRefreshWorker(service)
	service.onRefresh = func(id int64) {
		if id == 1 {
			w.Enqueue(99)
		}
	}

	w.drain()

	if want := []int64{1, 99, 2, 3}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}
}

// A claimed person whose on-demand lookup is pending is looked up once.
func TestPersonRefreshWorkerSkipsClaimedPersonQueuedOnDemand(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2}}}
	w := newTestPersonRefreshWorker(service)
	service.onRefresh = func(id int64) {
		if id == 1 {
			// Queued while the batch runs; it runs before 2 and covers it.
			w.Enqueue(2)
		}
	}

	w.drain()

	if want := []int64{1, 2}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}
}

// A stopped worker finishes the lookup in progress and starts no more.
func TestPersonRefreshWorkerStopsMidBatch(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2, 3}, {4, 5, 6}}}
	w := newTestPersonRefreshWorker(service)
	service.onRefresh = func(id int64) {
		if id == 1 {
			w.Stop()
		}
	}

	w.drain()

	if want := []int64{1}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}
}

// rateLimitErr is a lookup a provider rate limited.
type rateLimitErr struct{ retryAfter time.Duration }

func (e rateLimitErr) Error() string                      { return "rate limited" }
func (e rateLimitErr) RateLimitRetryAfter() time.Duration { return e.retryAfter }

// A rate-limited lookup ends the batch and pauses background claims, while
// on-demand lookups keep running (#1606).
func TestPersonRefreshWorkerPausesBackgroundLookupsOnRateLimit(t *testing.T) {
	service := &fakePersonRefresher{
		batches: [][]int64{{1, 2, 3}, {4, 5, 6}},
		errs:    map[int64]error{2: rateLimitErr{}},
	}
	w := newTestPersonRefreshWorker(service)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }

	w.drain()
	if want := []int64{1, 2}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v: the batch should stop at the rate limit", service.refreshed, want)
	}

	// Still paused: nothing is claimed, but a person page is looked up.
	w.Enqueue(9)
	w.drain()
	if service.claims != 1 {
		t.Fatalf("claims = %d while paused, want 1", service.claims)
	}
	if want := []int64{1, 2, 9}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}

	// After the pause, background lookups resume.
	now = now.Add(personRefreshRateLimitBackoff + time.Second)
	w.drain()
	if want := []int64{1, 2, 9, 4, 5, 6}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v after the pause", service.refreshed, want)
	}
}

// The pause doubles on each further limit up to its cap, follows a provider's
// longer RetryAfter, and resets after a lookup that isn't rate limited.
func TestPersonRefreshWorkerRateLimitBackoff(t *testing.T) {
	w := newTestPersonRefreshWorker(&fakePersonRefresher{})
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	pause := func() time.Duration { return w.pausedUntil.Sub(now) }

	for _, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		if !w.rateLimited(rateLimitErr{}) {
			t.Fatal("a rate-limit error didn't pause")
		}
		if got := pause(); got != want {
			t.Fatalf("pause = %s, want %s", got, want)
		}
	}
	w.rateLimited(nil)
	w.rateLimited(rateLimitErr{retryAfter: 30 * time.Minute})
	if got := pause(); got != 30*time.Minute {
		t.Fatalf("pause = %s, want the provider's 30m", got)
	}
	w.rateLimited(errors.New("timeout"))
	w.rateLimited(rateLimitErr{})
	if got := pause(); got != time.Minute {
		t.Fatalf("pause after a reset = %s, want 1m", got)
	}
}

func TestPersonRefreshWorkerSetRatePerMinute(t *testing.T) {
	w := newTestPersonRefreshWorker(&fakePersonRefresher{})
	w.SetRatePerMinute(60)
	if got := time.Duration(w.delay.Load()); got != time.Second {
		t.Fatalf("delay at 60/min = %s, want 1s", got)
	}
	w.SetRatePerMinute(0)
	if got := time.Duration(w.delay.Load()); got != time.Second {
		t.Fatalf("delay after an invalid rate = %s, want it unchanged at 1s", got)
	}
}

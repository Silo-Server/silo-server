package worker

import (
	"context"
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
	// since records the time each lookup was claimed or requested.
	since map[int64]time.Time
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

func (f *fakePersonRefresher) RefreshPersonUnlessStartedSince(ctx context.Context, id int64, since time.Time) (*models.Person, error) {
	f.mu.Lock()
	if f.since == nil {
		f.since = map[int64]time.Time{}
	}
	f.since[id] = since
	f.mu.Unlock()
	return f.RefreshPerson(ctx, id)
}

func newTestPersonRefreshWorker(service PersonRefresher) *PersonRefreshWorker {
	return NewPersonRefreshWorker(service, PersonRefreshWorkerConfig{BatchSize: 3})
}

// Slow lookups that use up the claim's lease stop the batch before another
// node can claim the people it hasn't reached.
func TestPersonRefreshWorkerStopsBatchWhenClaimLeaseRunsOut(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2, 3}}}
	w := NewPersonRefreshWorker(service, PersonRefreshWorkerConfig{
		BatchSize:      3,
		RefreshTimeout: 2 * time.Minute,
		ClaimLease:     time.Hour,
	})
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	service.onRefresh = func(int64) { now = now.Add(40 * time.Minute) }

	if more := w.processBatch(); !more {
		t.Fatal("processBatch() = false, want true so the sweep goes on")
	}
	// Person 2 starts 40 minutes in; person 3 would start 80 minutes in,
	// after the claim's lease ran out.
	if want := []int64{1, 2}; !slices.Equal(service.refreshed, want) {
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
			w.Enqueue(2, nil)
		}
	}

	w.drain()

	if want := []int64{1, 2}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}
}

// rateLimitErr is a lookup a provider rate limited.
type rateLimitErr struct{ retryAfter time.Duration }

func (e rateLimitErr) Error() string                      { return "rate limited" }
func (e rateLimitErr) RateLimitRetryAfter() time.Duration { return e.retryAfter }

// A slow rate claims only the people it can start before the claim's lease
// runs out.
func TestPersonRefreshWorkerClaimLimitFitsTheLease(t *testing.T) {
	w := NewPersonRefreshWorker(&fakePersonRefresher{}, PersonRefreshWorkerConfig{
		BatchSize:      100,
		RefreshTimeout: 2 * time.Minute,
		ClaimLease:     time.Hour,
	})
	for _, tc := range []struct {
		perMinute int
		want      int
	}{
		{perMinute: 120, want: 100},
		// One a minute starts at 0, 1, ..., 58 minutes: 59 before the last
		// safe start, 58 minutes into the hour.
		{perMinute: 1, want: 59},
	} {
		w.SetRatePerMinute(tc.perMinute)
		if got := w.claimLimit(); got != tc.want {
			t.Errorf("claimLimit() at %d/min = %d, want %d", tc.perMinute, got, tc.want)
		}
	}
}

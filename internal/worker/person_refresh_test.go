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

// Background lookups carry the claim time and page requests the attempt the
// page saw, so a person another node started looking up since is skipped.
func TestPersonRefreshWorkerPassesClaimAndRequestTimes(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2}}}
	w := newTestPersonRefreshWorker(service)
	seen := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	claimedAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return claimedAt }
	w.Enqueue(9, &seen)
	w.Enqueue(8, nil)

	w.drain()

	if want := []int64{9, 8, 1, 2}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}
	// A page request is cut off at the attempt the page saw.
	for id, want := range map[int64]time.Time{9: seen, 8: neverAttempted, 1: claimedAt, 2: claimedAt} {
		if got := service.since[id]; !got.Equal(want) {
			t.Fatalf("person %d since = %s, want %s", id, got, want)
		}
	}
}

// A person-page request that arrives during a batch runs before the rest of it.
func TestPersonRefreshWorkerRunsOnDemandRequestsBetweenBatchItems(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2, 3}}}
	w := newTestPersonRefreshWorker(service)
	service.onRefresh = func(id int64) {
		if id == 1 {
			w.Enqueue(99, nil)
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
			w.Enqueue(2, nil)
		}
	}

	w.drain()

	if want := []int64{1, 2}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}
}

// A stopped worker finishes the on-demand lookup in progress and starts no
// more of them.
func TestPersonRefreshWorkerStopsBetweenOnDemandRequests(t *testing.T) {
	service := &fakePersonRefresher{}
	w := newTestPersonRefreshWorker(service)
	for _, id := range []int64{1, 2, 3} {
		w.Enqueue(id, nil)
	}
	service.onRefresh = func(id int64) {
		if id == 1 {
			w.Stop()
		}
	}

	w.runManual(map[int64]struct{}{})

	if want := []int64{1}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
	}
}

// A worker stopped during an on-demand lookup doesn't claim a batch it
// won't look up: the claim would hold those people until its lease ran out.
func TestPersonRefreshWorkerStoppedDuringOnDemandClaimsNothing(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2, 3}}}
	w := newTestPersonRefreshWorker(service)
	w.Enqueue(99, nil)
	service.onRefresh = func(id int64) {
		if id == 99 {
			w.Stop()
		}
	}

	if w.processBatch() {
		t.Fatal("processBatch reported more work after a stop")
	}
	if service.claims != 0 {
		t.Fatalf("claimed %d batches after the stop, want none", service.claims)
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

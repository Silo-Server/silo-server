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

// A person page opened while the worker waits out a slow rate runs right
// away, and a claimed person it covers isn't looked up again after the wait.
func TestPersonRefreshWorkerRunsOnDemandRequestsDuringRateWait(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1, 2}}}
	w := NewPersonRefreshWorker(service, PersonRefreshWorkerConfig{BatchSize: 2})
	w.delay.Store(int64(time.Hour))
	pageDone := make(chan struct{})
	service.onRefresh = func(id int64) {
		switch id {
		case 1:
			// Person 2's lookup now waits an hour; a page for them opens.
			go w.Enqueue(2, nil)
		case 2:
			close(pageDone)
		}
	}
	finished := make(chan struct{})
	go func() {
		w.processBatch()
		close(finished)
	}()

	select {
	case <-pageDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the person-page request waited out the rate")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the batch didn't finish after the page request covered its last person")
	}
	if want := []int64{1, 2}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v", service.refreshed, want)
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

// A provider rate limiting a page's lookup pauses background lookups: before
// a batch is claimed, and between the items of one already claimed.
func TestPersonRefreshWorkerPausesOnOnDemandRateLimit(t *testing.T) {
	service := &fakePersonRefresher{
		batches: [][]int64{{1, 2, 3}},
		errs:    map[int64]error{99: rateLimitErr{retryAfter: 30 * time.Minute}},
	}
	w := newTestPersonRefreshWorker(service)
	w.Enqueue(99, nil)
	if w.processBatch() || service.claims != 0 || !w.paused() {
		t.Fatalf("claims %d, paused %v after a rate-limited page lookup; want no claim and a pause", service.claims, w.paused())
	}

	service = &fakePersonRefresher{
		batches: [][]int64{{1, 2, 3}},
		errs:    map[int64]error{99: rateLimitErr{}},
	}
	w = newTestPersonRefreshWorker(service)
	service.onRefresh = func(id int64) {
		if id == 1 {
			w.Enqueue(99, nil)
		}
	}
	w.processBatch()
	if want := []int64{1, 99}; !slices.Equal(service.refreshed, want) {
		t.Fatalf("refreshed %v, want %v: the batch should stop at the page's rate limit", service.refreshed, want)
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
	w.Enqueue(9, nil)
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
	// A rate limit without a Retry-After right after doesn't shorten it.
	w.rateLimited(rateLimitErr{})
	if got := pause(); got != 30*time.Minute {
		t.Fatalf("pause after a shorter rate limit = %s, want the provider's 30m", got)
	}

	now = now.Add(31 * time.Minute)
	w.rateLimited(errors.New("timeout"))
	w.rateLimited(rateLimitErr{})
	if got := pause(); got != time.Minute {
		t.Fatalf("pause after a reset = %s, want 1m", got)
	}
}

// The rate holds across batches: the first lookup of the next batch waits
// out the delay after the last lookup of the one before.
func TestPersonRefreshWorkerPacesLookupsAcrossBatches(t *testing.T) {
	service := &fakePersonRefresher{batches: [][]int64{{1}, {2}, {3}}}
	w := NewPersonRefreshWorker(service, PersonRefreshWorkerConfig{BatchSize: 1})
	const delay = 30 * time.Millisecond
	w.delay.Store(int64(delay))
	var starts []time.Time
	service.onRefresh = func(int64) { starts = append(starts, time.Now()) }

	w.drain()

	if len(starts) != 3 {
		t.Fatalf("refreshed %v, want [1 2 3]", service.refreshed)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < delay {
			t.Fatalf("lookup %d started %s after the one before, want at least %s", i+1, gap, delay)
		}
	}
}

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

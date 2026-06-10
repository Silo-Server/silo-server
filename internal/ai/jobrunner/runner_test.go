package jobrunner

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStore struct {
	mu         sync.Mutex
	heartbeats int
	resets     int
	lastBefore time.Time
}

func (s *fakeStore) Heartbeat(context.Context, int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats++
	return nil
}

func (s *fakeStore) ResetStaleJobs(_ context.Context, before time.Time, _ string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resets++
	s.lastBefore = before
	return 0, nil
}

func (s *fakeStore) snapshot() (int, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resets, s.lastBefore
}

func TestRecoverReapsImmediatelyWithStaleCutoff(t *testing.T) {
	store := &fakeStore{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	approxNow := time.Now()
	New(ctx, nil, store, "test", nil).Recover()

	resets, before := store.snapshot()
	if resets < 1 {
		t.Fatalf("Recover did not reap immediately: resets=%d", resets)
	}
	want := approxNow.Add(-StaleJobThreshold)
	if diff := before.Sub(want); diff > 2*time.Second || diff < -2*time.Second {
		t.Errorf("stale cutoff = %v, want ~%v", before, want)
	}
}

// One shared semaphore bounds jobs across BOTH runners: with size 1, the
// second runner's job must not start until the first runner's job finishes.
func TestSharedSemaphoreBoundsAcrossRunners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sem := NewSemaphore(1)
	r1 := New(ctx, sem, &fakeStore{}, "one", nil)
	r2 := New(ctx, sem, &fakeStore{}, "two", nil)

	release := make(chan struct{})
	aStarted := make(chan struct{})
	var bStarted atomic.Bool
	bDone := make(chan struct{})

	r1.Dispatch(1, func(context.Context) {
		close(aStarted)
		<-release
	}, nil)
	<-aStarted

	r2.Dispatch(2, func(context.Context) {
		bStarted.Store(true)
		close(bDone)
	}, nil)

	time.Sleep(50 * time.Millisecond)
	if bStarted.Load() {
		t.Fatal("job B ran while job A held the only slot")
	}
	close(release)
	select {
	case <-bDone:
	case <-time.After(2 * time.Second):
		t.Fatal("job B never ran after the slot freed")
	}
}

// Cancelling a job that is queued behind the semaphore aborts it without
// running it, via the onAbort callback.
func TestCancelWhileQueuedCallsOnAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sem := NewSemaphore(1)
	r := New(ctx, sem, &fakeStore{}, "test", nil)

	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	r.Dispatch(1, func(context.Context) {
		close(started)
		<-release
	}, nil)
	<-started

	var ran atomic.Bool
	aborted := make(chan struct{})
	r.Dispatch(2, func(context.Context) {
		ran.Store(true)
	}, func(context.Context) {
		close(aborted)
	})

	if !r.Cancel(2) {
		t.Fatal("Cancel(2) found no in-flight goroutine")
	}
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("onAbort never ran")
	}
	if ran.Load() {
		t.Fatal("cancelled queued job still ran")
	}
}

func TestCancelUnknownJobReturnsFalse(t *testing.T) {
	r := New(context.Background(), nil, &fakeStore{}, "test", nil)
	if r.Cancel(99) {
		t.Fatal("Cancel(99) = true for unknown job")
	}
}

// The run context is cancelled by Cancel(id) so an in-flight job can stop.
func TestCancelRunningJobCancelsContext(t *testing.T) {
	r := New(context.Background(), NewSemaphore(1), &fakeStore{}, "test", nil)
	started := make(chan struct{})
	stopped := make(chan struct{})
	r.Dispatch(7, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	}, nil)
	<-started
	if !r.Cancel(7) {
		t.Fatal("Cancel(7) found no in-flight goroutine")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("running job did not observe cancellation")
	}
}

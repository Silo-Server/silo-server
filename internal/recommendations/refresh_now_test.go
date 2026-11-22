package recommendations

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// blockingRefresh is a profile refresh that runs until released, recording
// the error of the context it ran with.
type blockingRefresh struct {
	started  chan struct{}
	release  chan struct{}
	finished chan error
}

func newBlockingRefresh() *blockingRefresh {
	return &blockingRefresh{started: make(chan struct{}, 1), release: make(chan struct{}), finished: make(chan error, 1)}
}

func (b *blockingRefresh) run(ctx context.Context, _ clusterLock, _ int, _ string) error {
	b.started <- struct{}{}
	<-b.release
	b.finished <- ctx.Err()
	return nil
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// eventually polls cond until it holds, failing after a few seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// RefreshProfileNow waits for the refresh it starts, and holds the profile's
// pending key while it runs, so neither the queue nor a second call starts a
// duplicate on this server; their requests make it run once more instead.
func TestRefreshNowWaitsAndHoldsThePendingKey(t *testing.T) {
	w, _ := newRefreshTestWorker()
	ctx := t.Context()
	refresh := newBlockingRefresh()

	result := make(chan bool, 1)
	go func() { result <- w.refreshNow(ctx, 7, "p", time.Hour, refresh.run) }()
	receive(t, refresh.started, "the refresh to start")

	w.RequestProfileRefresh(ctx, 7, "p")
	assertNothingQueued(t, w, "queue request during the refresh")
	if w.refreshNow(ctx, 7, "p", time.Hour, func(context.Context, clusterLock, int, string) error {
		t.Error("a second refresh ran beside the first")
		return nil
	}) {
		t.Fatal("a second call reported a refresh it did not run")
	}

	close(refresh.release)
	receive(t, refresh.finished, "the refresh to finish")
	// The requests made while it ran make it run once more.
	receive(t, refresh.finished, "the refresh to run again")
	if !receive(t, result, "the call to return") {
		t.Fatal("call reported the finished refresh as unfinished")
	}
	if !w.claimProfileRefresh(profileRefreshKey(7, "p")) {
		t.Fatal("the pending key outlived the refresh")
	}
	if n := w.refreshesNow.Load(); n != 0 {
		t.Fatalf("%d refresh slots still held", n)
	}
}

// A refresh request made while a profile's refresh runs makes it run once
// more as soon as it ends, instead of leaving the change to the stale sweep;
// a request made while the refresh is only queued is read by it, so it adds
// no run.
func TestProfileRefreshRunsAgainForAChangeMadeWhileItRuns(t *testing.T) {
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = &Engine{}
	refresh := newBlockingRefresh()
	key := profileRefreshKey(1, "p")
	if !w.claimProfileRefresh(key) {
		t.Fatal("could not claim the profile refresh")
	}
	w.RequestProfileRefresh(t.Context(), 1, "p") // while queued

	done := make(chan struct{})
	go func() {
		_ = w.runProfileRefresh(context.Background(), nil, 1, "p", refresh.run)
		close(done)
	}()
	receive(t, refresh.started, "the first run")
	w.RequestProfileRefresh(t.Context(), 1, "p") // while running
	refresh.release <- struct{}{}
	receive(t, refresh.finished, "the first run to finish")

	receive(t, refresh.started, "a second run")
	refresh.release <- struct{}{}
	receive(t, refresh.finished, "the second run to finish")
	receive(t, done, "the refresh to end after two runs")
	if !w.claimProfileRefresh(key) {
		t.Fatal("the pending key was not released")
	}
}

// A profile still changing after profileRefreshMaxRuns runs is left for the
// stale sweep: the refresh stops and releases the key rather than looping
// for as long as the changes keep coming.
func TestProfileRefreshStopsRerunningAfterTheCap(t *testing.T) {
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = &Engine{}
	refresh := newBlockingRefresh()
	key := profileRefreshKey(1, "p")
	if !w.claimProfileRefresh(key) {
		t.Fatal("could not claim the profile refresh")
	}
	done := make(chan struct{})
	go func() {
		_ = w.runProfileRefresh(context.Background(), nil, 1, "p", refresh.run)
		close(done)
	}()
	for run := 1; run <= profileRefreshMaxRuns; run++ {
		receive(t, refresh.started, fmt.Sprintf("run %d", run))
		w.RequestProfileRefresh(t.Context(), 1, "p") // a change on every run
		refresh.release <- struct{}{}
		receive(t, refresh.finished, fmt.Sprintf("run %d to finish", run))
	}
	receive(t, done, "the refresh to stop at the cap")
	if !w.claimProfileRefresh(key) {
		t.Fatal("the pending key was not released")
	}
}

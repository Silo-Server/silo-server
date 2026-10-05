package recommendations

import (
	"context"
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

func (b *blockingRefresh) run(ctx context.Context, _ int, _ string) error {
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
	if w.refreshNow(ctx, 7, "p", time.Hour, func(context.Context, int, string) error {
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

// A refresh slower than the wait keeps running after the call returns, with
// a context the caller cannot cancel, and is not queued again. The same holds
// when the caller goes away first.
func TestRefreshNowLeavesASlowRefreshRunning(t *testing.T) {
	for name, waitFor := range map[string]time.Duration{"wait runs out": time.Millisecond, "caller leaves": time.Hour} {
		t.Run(name, func(t *testing.T) {
			w, _ := newRefreshTestWorker()
			ctx, cancel := context.WithCancel(t.Context())
			refresh := newBlockingRefresh()

			result := make(chan bool, 1)
			go func() { result <- w.refreshNow(ctx, 7, "p", waitFor, refresh.run) }()
			receive(t, refresh.started, "the refresh to start")
			cancel()
			if receive(t, result, "the call to return") {
				t.Fatal("call reported a refresh that was still running")
			}
			if w.claimProfileRefresh(profileRefreshKey(7, "p")) {
				t.Fatal("the pending key was released while the refresh ran")
			}

			close(refresh.release)
			if err := receive(t, refresh.finished, "the refresh to finish"); err != nil {
				t.Fatalf("refresh context ended with the caller: %v", err)
			}
			eventually(t, "the refresh to release its key and slot", func() bool {
				return w.refreshesNow.Load() == 0 && w.claimProfileRefresh(profileRefreshKey(7, "p"))
			})
			assertNothingQueued(t, w, "after a slow refresh")
		})
	}
}

// Past maxRefreshesNow running refreshes, a call queues the refresh instead
// of starting another.
func TestRefreshNowQueuesWhenTheServerIsBusy(t *testing.T) {
	w, _ := newRefreshTestWorker()
	w.refreshesNow.Store(maxRefreshesNow)
	if w.refreshNow(t.Context(), 7, "p", time.Hour, func(context.Context, int, string) error {
		t.Error("refresh ran past the limit")
		return nil
	}) {
		t.Fatal("call reported a refresh it did not run")
	}
	if req := takeQueued(t, w); req.userID != 7 || req.profileID != "p" {
		t.Fatalf("queued %+v", req)
	}
	if n := w.refreshesNow.Load(); n != maxRefreshesNow {
		t.Fatalf("slots = %d, want %d", n, maxRefreshesNow)
	}
}

// RefreshProfileNow marks the profile stale before it refreshes, as
// NotifySignalsChanged does, so a server that dies mid-refresh leaves the
// mark for another server's sweep. A failed refresh marks it again.
func TestRefreshProfileNowMarksStaleFirst(t *testing.T) {
	w, marker := newRefreshTestWorker()
	marker.w = nil
	w.engine = closedPoolEngine(t)
	if !w.RefreshProfileNow(t.Context(), 7, "p") {
		t.Fatal("refresh against a closed database did not finish within the wait")
	}
	if marker.calls != 2 {
		t.Fatalf("stale marks = %d, want one before the refresh and one after it failed", marker.calls)
	}

	var disabled *Worker
	if disabled.RefreshProfileNow(t.Context(), 7, "p") {
		t.Fatal("a nil worker reported a refresh")
	}
	if w.RefreshProfileNow(t.Context(), 0, "p") || w.RefreshProfileNow(t.Context(), 7, "") {
		t.Fatal("an invalid profile was refreshed")
	}
	if marker.calls != 2 {
		t.Fatalf("stale marks for invalid profiles = %d, want none", marker.calls-2)
	}
}

// A taste-seed request canceled after its picks were committed still leaves
// the stale mark, written with a live context, and still queues the refresh
// when the server is too busy to start it, so the picks are applied.
func TestRefreshProfileNowMarksAndQueuesAfterTheCallerGaveUp(t *testing.T) {
	w, marker := newRefreshTestWorker()
	marker.w = nil
	w.refreshesNow.Store(maxRefreshesNow)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if w.RefreshProfileNow(ctx, 7, "p") {
		t.Fatal("call reported a refresh it did not run")
	}
	if marker.calls != 1 || marker.ctxErrs[0] != nil {
		t.Fatalf("stale marks = %d with context errors %v; want one with a live context", marker.calls, marker.ctxErrs)
	}
	if req := takeQueued(t, w); req.userID != 7 || req.profileID != "p" {
		t.Fatalf("queued %+v", req)
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
		w.runProfileRefresh(context.Background(), 1, "p", refresh.run)
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

package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSyncNowSerializesSnapshotCapture guards the SyncNow ordering contract:
// snapshot capture and reconciliation run under one lock, so a request-path
// sync (playback start/stop) can never interleave with the periodic tick and
// commit an older session snapshot after a newer one.
func TestSyncNowSerializesSnapshotCapture(t *testing.T) {
	var inflight atomic.Int32
	var overlapped atomic.Bool
	provider := func() []SessionSync {
		if inflight.Add(1) > 1 {
			overlapped.Store(true)
		}
		time.Sleep(2 * time.Millisecond)
		inflight.Add(-1)
		return nil
	}

	// No pool is needed: an empty snapshot with no node name returns before
	// any database work, keeping the test focused on the locking contract.
	r := NewReconciler(nil, "", provider)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.SyncNow(context.Background()); err != nil {
				t.Errorf("SyncNow: %v", err)
			}
		}()
	}
	wg.Wait()

	if overlapped.Load() {
		t.Fatal("concurrent SyncNow calls captured snapshots concurrently; capture and reconcile must be serialized")
	}
}

// TestSyncNowCoalescesPendingPass guards the follow-up contract: a SyncNow
// call that arrives while a sync is in flight returns immediately, and the
// running owner re-captures a fresh snapshot afterwards — so a stop that lands
// mid-sync is still reflected without waiting for the periodic tick.
func TestSyncNowCoalescesPendingPass(t *testing.T) {
	captures := make(chan struct{}, 16)
	release := make(chan struct{})
	first := true
	provider := func() []SessionSync {
		captures <- struct{}{}
		if first {
			first = false
			<-release // hold the first sync mid-flight
		}
		return nil
	}
	r := NewReconciler(nil, "", provider)

	ownerDone := make(chan error, 1)
	go func() { ownerDone <- r.SyncNow(context.Background()) }()
	<-captures // owner is now blocked inside its snapshot capture

	// A second sync while the first is in flight must not block.
	done := make(chan struct{})
	go func() {
		_ = r.SyncNow(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SyncNow blocked behind an in-flight sync; it must coalesce and return")
	}

	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatalf("owner SyncNow: %v", err)
	}
	select {
	case <-captures: // the owner's follow-up pass with a fresh snapshot
	default:
		t.Fatal("no follow-up snapshot capture ran; the coalesced sync was lost")
	}
}

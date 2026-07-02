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

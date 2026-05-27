package audiobooks

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEnricherRunFansOut verifies that runBatch processes a claimed batch with
// multiple workers in flight at once. The test installs a fake enrichItem path
// that blocks until N concurrent calls are observed; if runBatch is still
// serial, the test will time out and fail the max-in-flight assertion.
func TestEnricherRunFansOut(t *testing.T) {
	const wantWorkers = 4
	const itemCount = 16

	items := make([]enrichmentItemRow, itemCount)
	for i := range items {
		items[i] = enrichmentItemRow{ContentID: "test", Title: "t"}
	}

	var inFlight int32
	var maxInFlight int32
	var signaled int32
	var wg sync.WaitGroup
	wg.Add(wantWorkers)
	gate := make(chan struct{})

	enrich := func(ctx context.Context, item enrichmentItemRow) error {
		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			prev := atomic.LoadInt32(&maxInFlight)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, cur) {
				break
			}
		}
		// First wantWorkers callers release the gate; later ones don't block.
		if atomic.AddInt32(&signaled, 1) <= wantWorkers {
			wg.Done()
		}
		select {
		case <-gate:
		case <-time.After(2 * time.Second):
		}
		return nil
	}

	go func() {
		wg.Wait()
		close(gate)
	}()

	e := &Enricher{workers: wantWorkers, batchSize: itemCount}
	e.runBatch(context.Background(), items, enrich)

	if got := atomic.LoadInt32(&maxInFlight); got < wantWorkers {
		t.Errorf("max in-flight = %d, want >= %d", got, wantWorkers)
	}
}

package audiobooks

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// Shutdown during the gap between batches ends the sweep at once, without
// another claim and without waiting out the gap.
func TestSweepStopsDuringTheGapOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &Enricher{batchSize: 2}
		ctx, cancel := context.WithCancel(t.Context())
		runs := 0
		runBatch := func(context.Context) (sweepBatch, error) {
			runs++
			return sweepBatch{claimed: 2, summary: sweepSummary{enriched: 2}}, nil
		}
		done := make(chan struct{})
		var got int
		var err error
		start := time.Now()
		go func() {
			got, err = e.sweep(ctx, func(context.Context) error { return nil }, runBatch, sleepCtx)
			close(done)
		}()
		synctest.Wait() // the sweep is now waiting out the gap after batch 1
		cancel()
		<-done
		if err != nil || runs != 1 || got != 2 {
			t.Fatalf("sweep = (%d, %v) after %d batches, want (2, nil) after 1", got, err, runs)
		}
		if waited := time.Since(start); waited != 0 {
			t.Fatalf("sweep took %v to stop, want no wait", waited)
		}
	})
}

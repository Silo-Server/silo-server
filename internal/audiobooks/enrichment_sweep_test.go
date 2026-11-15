package audiobooks

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// The sweep keeps claiming batches while full ones come back and stops on
// each exit condition without claiming another.
func TestSweepExitConditions(t *testing.T) {
	const size = 4
	full := sweepBatch{claimed: size, summary: sweepSummary{enriched: 3, noMatch: 1}}
	tests := []struct {
		name     string
		batches  []sweepBatch
		lockLost bool
		wantRuns int
		wantSum  int
	}{
		{name: "nothing due", batches: []sweepBatch{{}}, wantRuns: 1},
		{
			name:     "drains full batches until a short one",
			batches:  []sweepBatch{full, full, {claimed: 1, summary: sweepSummary{enriched: 1}}},
			wantRuns: 3, wantSum: 7,
		},
		{
			name:     "whole batch deferred, as behind a paused chain",
			batches:  []sweepBatch{{claimed: size}, full},
			wantRuns: 1,
		},
		{
			name:     "whole batch failed",
			batches:  []sweepBatch{{claimed: size, summary: sweepSummary{retrying: 2, failed: 2}}, full},
			wantRuns: 1,
		},
		{name: "lock lost", batches: []sweepBatch{full, full}, lockLost: true, wantRuns: 1, wantSum: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := &Enricher{batchSize: size}
				runs := 0
				var gaps []time.Duration
				last := time.Now()
				runBatch := func(context.Context) (sweepBatch, error) {
					if runs > 0 {
						gaps = append(gaps, time.Since(last))
					}
					runs++
					if runs > len(tt.batches) {
						t.Fatalf("batch %d claimed after the sweep should have stopped", runs)
					}
					last = time.Now()
					return tt.batches[runs-1], nil
				}
				lockHeld := func(context.Context) error {
					if tt.lockLost {
						return errors.New("session closed")
					}
					return nil
				}
				got, err := e.sweep(t.Context(), lockHeld, runBatch, sleepCtx)
				if err != nil {
					t.Fatalf("err = %v, want nil: work already recorded stands", err)
				}
				if runs != tt.wantRuns || got != tt.wantSum {
					t.Fatalf("batches = %d, enriched = %d; want %d and %d", runs, got, tt.wantRuns, tt.wantSum)
				}
				for _, gap := range gaps {
					if gap != sweepBatchGap {
						t.Fatalf("next batch started %v after the last one, want %v", gap, sweepBatchGap)
					}
				}
			})
		})
	}
}

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

package mediasample

import (
	"context"
	"testing"
	"time"
)

// stubKeyframes makes lookups return keyframes and counts them.
func stubKeyframes(t *testing.T, keyframes []float64) *int {
	t.Helper()
	calls := 0
	previous := lookupKeyframes
	lookupKeyframes = func(context.Context, string) []float64 {
		calls++
		return keyframes
	}
	t.Cleanup(func() { lookupKeyframes = previous })
	return &calls
}

// TestSheetKeyframesStopsWaiting blocks a lookup, as stalled storage
// would: a canceled run goes on without keyframes at once, and the lookup
// sees its context end.
func TestSheetKeyframesStopsWaiting(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	previous := lookupKeyframes
	lookupKeyframes = func(ctx context.Context, _ string) []float64 {
		defer close(exited)
		close(entered)
		<-ctx.Done()
		return []float64{0}
	}
	t.Cleanup(func() { lookupKeyframes = previous })
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-entered
		cancel()
	}()
	if got := sheetKeyframes(ctx, sheetsRequest([]float64{5}, 1, 1)); got != nil {
		t.Fatalf("keyframes %v, want none", got)
	}
	<-exited
}

// TestSheetKeyframesLeavesACanceledRunAlone never touches the input of a
// run canceled before it starts.
func TestSheetKeyframesLeavesACanceledRunAlone(t *testing.T) {
	calls := stubKeyframes(t, []float64{0})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := sheetKeyframes(ctx, sheetsRequest([]float64{5}, 1, 1)); got != nil || *calls != 0 {
		t.Fatalf("keyframes %v after %d lookups, want none", got, *calls)
	}
}

// TestSheetsDiscardsAnAbandonedListAfterTheAttemptEnds abandons a keyframe
// list whose ffmpeg exited cleanly after the attempt's time ran out, while
// the caller still waits: its cells are not published, and the attempt fails
// as timed out instead of reading the samples again.
func TestSheetsDiscardsAnAbandonedListAfterTheAttemptEnds(t *testing.T) {
	keyframeRun := &fakeSheets{frames: []fakeSheetFrame{at(30, 25, "1"), at(10, 5, "1.5"), at(20, 15, "2")}}
	exec, calls := runSequence([]*fakeSheets{keyframeRun, {}, {}, {}}, nil)
	// A deadline in the past ends the attempt at once; the fake ffmpeg
	// still writes its frames and exits cleanly.
	attemptCtx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	run := attemptRun{runner: Runner{Exec: exec}, ctx: t.Context(), attemptCtx: attemptCtx, decoder: softwareDecoder, keyframes: []float64{0, 10, 20}}
	req := sheetsRequest(secondsFrom(5, 10, 3), 3, 1)
	result, failure := run.sheets(req, req.Samples.Seconds, 0)
	if failure == nil || failure.Reason != ReasonTimeout || len(result.Sheets) != 0 || len(*calls) != 1 {
		t.Fatalf("frames %+v failure %v after %d runs, want the abandoned list alone, timed out without sheets", result.SheetFrames, failure, len(*calls))
	}
}

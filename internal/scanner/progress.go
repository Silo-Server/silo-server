package scanner

import (
	"context"
	"sync"
	"time"
)

// ProgressUpdate captures scanner-phase progress for long-running scans.
type ProgressUpdate struct {
	Phase           string
	Message         string
	CurrentScope    string
	TotalFiles      int
	FilesDiscovered int
	FilesProcessed  int
	New             int
	Updated         int
	Unchanged       int
	Errors          int
}

type progressReporterKey struct{}

type progressReporter func(ProgressUpdate)

// WithProgressReporter attaches an optional scanner progress reporter to ctx.
func WithProgressReporter(ctx context.Context, reporter func(ProgressUpdate)) context.Context {
	if ctx == nil || reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressReporterKey{}, progressReporter(reporter))
}

func reportProgress(ctx context.Context, update ProgressUpdate) {
	if ctx == nil {
		return
	}
	reporter, _ := ctx.Value(progressReporterKey{}).(progressReporter)
	if reporter == nil {
		return
	}
	reporter(update)
}

// reportProgressEvery reports progress() every interval until stop is called
// or ctx ends, skipping a report identical to the one before it. A scan's
// workers only bump counters; this goroutine owns the writes, so their rate
// follows the clock rather than the number of items. stop waits for a report
// already in flight.
func reportProgressEvery(ctx context.Context, interval time.Duration, progress func() ProgressUpdate) (stop func()) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		var last ProgressUpdate
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				if update := progress(); update != last {
					reportProgress(ctx, update)
					last = update
				}
			}
		}
	}()
	return sync.OnceFunc(func() {
		close(stopCh)
		<-done
	})
}

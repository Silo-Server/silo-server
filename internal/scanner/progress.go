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
// or ctx ends. A scan's workers only bump counters; this goroutine owns the
// writes, so their rate follows the clock rather than the number of items.
// Every tick writes, even with nothing new, so a write that failed is retried
// while a slow item holds the counts still. stop waits for a report already
// in flight.
func reportProgressEvery(ctx context.Context, interval time.Duration, progress func() ProgressUpdate) (stop func()) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				reportProgress(ctx, progress())
			}
		}
	}()
	return sync.OnceFunc(func() {
		close(stopCh)
		<-done
	})
}

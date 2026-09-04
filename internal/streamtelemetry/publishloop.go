package streamtelemetry

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// publishLoop is the publish-on-a-ticker lifecycle both of a process's
// publishers run: the measuring Registry and the ReportedPublisher.
//
// The two had hand-rolled the identical ticker, stop-channel, wait-for-exit and
// leave-the-roster sequence, which meant a fix to either had to be applied twice
// and a missed site left one publisher shutting down differently from the other.
// Leaving is the part that matters most and is easiest to get subtly wrong: a
// publisher that goes away without removing itself keeps its last snapshot
// readable until freshness expires and then lingers in the roster until
// membership does, so a graceful shutdown shows operators ghost live sessions
// followed by a degraded view — the exact thing the reporting publisher exists to
// eliminate.
type publishLoop struct {
	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}
	started   atomic.Bool

	leaveMu sync.Mutex
	left    bool
}

func newPublishLoop() *publishLoop {
	return &publishLoop{stop: make(chan struct{}), done: make(chan struct{})}
}

// run starts the ticker goroutine, at most once. publish is called with the tick
// instant, which is the capture time the snapshot is stamped with; it is never
// called concurrently with itself.
func (l *publishLoop) run(ctx context.Context, interval time.Duration, publish func(context.Context, time.Time)) {
	l.startOnce.Do(func() {
		l.started.Store(true)
		go func() {
			defer close(l.done)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-l.stop:
					return
				case at := <-ticker.C:
					publish(ctx, at)
				}
			}
		}()
	})
}

// halt ends publishing, waits for the loop to exit, and leaves the roster exactly
// once. A loop that never ran has nothing to stop and nothing to leave.
func (l *publishLoop) halt(ctx context.Context, store SnapshotStore) error {
	if l == nil || !l.started.Load() {
		return nil
	}
	l.stopOnce.Do(func() { close(l.stop) })
	select {
	case <-l.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	global, ok := store.(GlobalSnapshotStore)
	if !ok {
		return nil
	}
	l.leaveMu.Lock()
	defer l.leaveMu.Unlock()
	if l.left {
		return nil
	}
	if err := global.Leave(ctx); err != nil {
		return err
	}
	l.left = true
	return nil
}

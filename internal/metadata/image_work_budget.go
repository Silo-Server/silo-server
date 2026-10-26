package metadata

import (
	"context"
	"slices"
	"sync"
)

// imageWorkBudget bounds the artwork jobs one process runs at once across the
// image-cache drain, manual refreshes and discovery copies. Each job can
// decode a large image and encode its variant ladder, so metadata.image_workers
// and its memory cap must hold for all of them together.
//
// It counts running jobs across limit changes: after the limit is lowered, no
// job starts until enough running ones have finished. Waiting callers get
// freed slots first come, first served, so a caller that just released a slot
// cannot take it straight back ahead of them; an interactive caller (a manual
// refresh) waits only behind other interactive callers.
type imageWorkBudget struct {
	mu      sync.Mutex
	limit   int // zero: unbounded
	running int
	waiting []*imageWorkWaiter
}

type imageWorkWaiter struct {
	interactive bool
	granted     bool
	ready       chan struct{}
}

// set changes the limit; zero leaves jobs unbounded.
func (b *imageWorkBudget) set(limit int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limit = max(limit, 0)
	b.grantLocked()
}

// acquire waits for a slot and returns the function that frees it. A free
// slot nobody is waiting for is taken even when ctx is already done.
func (b *imageWorkBudget) acquire(ctx context.Context) (release func(), err error) {
	return b.acquireFor(ctx, false)
}

// acquireFor is acquire for an interactive caller when interactive is set.
func (b *imageWorkBudget) acquireFor(ctx context.Context, interactive bool) (release func(), err error) {
	b.mu.Lock()
	if len(b.waiting) == 0 && b.freeLocked() {
		b.running++
		b.mu.Unlock()
		return b.releaser(), nil
	}
	w := &imageWorkWaiter{interactive: interactive, ready: make(chan struct{})}
	at := len(b.waiting)
	if interactive {
		if i := slices.IndexFunc(b.waiting, func(q *imageWorkWaiter) bool { return !q.interactive }); i >= 0 {
			at = i
		}
	}
	b.waiting = slices.Insert(b.waiting, at, w)
	b.mu.Unlock()

	select {
	case <-w.ready:
		return b.releaser(), nil
	case <-ctx.Done():
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if w.granted {
		// Granted while giving up: hand the slot on.
		b.running--
		b.grantLocked()
	} else if i := slices.Index(b.waiting, w); i >= 0 {
		b.waiting = slices.Delete(b.waiting, i, i+1)
	}
	return nil, ctx.Err()
}

func (b *imageWorkBudget) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.running--
			b.grantLocked()
		})
	}
}

func (b *imageWorkBudget) freeLocked() bool {
	return b.limit == 0 || b.running < b.limit
}

// grantLocked gives free slots to the oldest waiting callers.
func (b *imageWorkBudget) grantLocked() {
	for len(b.waiting) > 0 && b.freeLocked() {
		w := b.waiting[0]
		b.waiting = b.waiting[1:]
		b.running++
		w.granted = true
		close(w.ready)
	}
}

// SetWorkerBudget sets how many artwork jobs this process runs at once, across
// the queue drain, manual refreshes and copies of discovered local artwork.
// Zero leaves them unbounded beyond each caller's own concurrency.
func (p *ImageCacheProcessor) SetWorkerBudget(workers int) {
	if p == nil {
		return
	}
	p.workBudget.set(workers)
}

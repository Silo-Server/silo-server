package metadata

import (
	"context"
	"sync"
)

// imageWorkBudget bounds the artwork jobs one process runs at once across the
// image-cache drain, manual refreshes and discovery copies. Each job can
// decode a large image and encode its variant ladder, so metadata.image_workers
// and its memory cap must hold for all of them together.
//
// It counts running jobs across limit changes: after the limit is lowered, no
// job starts, waiting ones included, until enough running ones have finished.
type imageWorkBudget struct {
	mu      sync.Mutex
	limit   int // zero: unbounded
	running int
	// changed is closed, and replaced on demand, when a slot frees or the
	// limit changes, so waiting callers check again.
	changed chan struct{}
}

// set changes the limit; zero leaves jobs unbounded.
func (b *imageWorkBudget) set(limit int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limit = max(limit, 0)
	b.wakeLocked()
}

// acquire waits for a slot and returns the function that frees it. A free
// slot is taken even when ctx is already done.
func (b *imageWorkBudget) acquire(ctx context.Context) (release func(), err error) {
	for {
		b.mu.Lock()
		if b.limit == 0 || b.running < b.limit {
			b.running++
			b.mu.Unlock()
			var once sync.Once
			return func() { once.Do(b.releaseOne) }, nil
		}
		if b.changed == nil {
			b.changed = make(chan struct{})
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (b *imageWorkBudget) releaseOne() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running--
	b.wakeLocked()
}

func (b *imageWorkBudget) wakeLocked() {
	if b.changed != nil {
		close(b.changed)
		b.changed = nil
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

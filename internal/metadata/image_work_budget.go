package metadata

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

// imageWorkBudget bounds the artwork jobs one process runs at once across the
// image-cache drain, manual refreshes and discovery copies. Each job can
// decode a large image and encode its variant ladder, so metadata.image_workers
// and its memory cap must hold for all of them together.
type imageWorkBudget struct {
	mu    sync.Mutex
	limit int
	sem   *semaphore.Weighted // nil until a limit is set: unbounded
}

// set changes the budget. Jobs already running keep the slot they took, so
// the process can briefly exceed a lowered budget.
func (b *imageWorkBudget) set(limit int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit == b.limit {
		return
	}
	b.limit = limit
	b.sem = nil
	if limit > 0 {
		b.sem = semaphore.NewWeighted(int64(limit))
	}
}

// acquire waits for a slot and returns the function that frees it.
func (b *imageWorkBudget) acquire(ctx context.Context) (release func(), err error) {
	b.mu.Lock()
	sem := b.sem
	b.mu.Unlock()
	if sem == nil {
		return func() {}, nil
	}
	if err := sem.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	return func() { sem.Release(1) }, nil
}

// SetWorkerBudget sets how many artwork jobs this process runs at once, across
// the queue drain, manual refreshes and copies of discovered local artwork.
// Zero leaves them unbounded beyond each caller's own concurrency.
func (p *ImageCacheProcessor) SetWorkerBudget(workers int) {
	if p == nil {
		return
	}
	p.workBudget.set(max(workers, 0))
}

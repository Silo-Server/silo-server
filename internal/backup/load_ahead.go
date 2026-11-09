package backup

import (
	"context"
	"runtime"

	"golang.org/x/sync/semaphore"
)

// Account documents are decoded and validated on the CPU, one at a time
// per account, which on a server with hundreds of accounts takes longer
// than comparing them with the database. loadAhead decodes the next ones
// on other cores while the caller works through them in order.

// loadAheadBytes bounds the document bytes loads in flight or waiting
// hold; a document larger than that loads alone.
const loadAheadBytes = 32 << 20

type loaded[T any] struct {
	v   T
	err error
}

// aheadLoads hands back load(0..n-1) in order, computed ahead on other
// goroutines.
type aheadLoads[T any] struct {
	out     []chan loaded[T]
	weights []int64
	budget  *semaphore.Weighted
	next    int
	stop    context.CancelFunc
}

// loadAhead runs load for each index from 0 to n-1 on up to GOMAXPROCS
// goroutines, at most loadAheadBytes of sizes in flight or waiting. Take
// each result with take, in order, and call close when done.
func loadAhead[T any](n int, sizes func(i int) int64, load func(i int) (T, error)) *aheadLoads[T] {
	ctx, stop := context.WithCancel(context.Background()) //nolint:gosec // G118: close calls stop
	a := &aheadLoads[T]{out: make([]chan loaded[T], n), weights: make([]int64, n), budget: semaphore.NewWeighted(loadAheadBytes), stop: stop}
	for i := range n {
		a.out[i] = make(chan loaded[T], 1)
		a.weights[i] = min(max(sizes(i), 1), loadAheadBytes)
	}
	workers := semaphore.NewWeighted(int64(max(runtime.GOMAXPROCS(0), 1)))
	go func() {
		for i := range n {
			if a.budget.Acquire(ctx, a.weights[i]) != nil {
				return
			}
			if workers.Acquire(ctx, 1) != nil {
				a.budget.Release(a.weights[i])
				return
			}
			go func() {
				defer workers.Release(1)
				v, err := load(i)
				a.out[i] <- loaded[T]{v, err}
			}()
		}
	}()
	return a
}

// take returns the next index's result, waiting for it.
func (a *aheadLoads[T]) take() (T, error) {
	r := <-a.out[a.next]
	a.budget.Release(a.weights[a.next])
	a.next++
	return r.v, r.err
}

// close stops starting loads; those running finish into their buffers.
func (a *aheadLoads[T]) close() { a.stop() }

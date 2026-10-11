package downloadstorage

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"time"
)

// ErrDirBusy reports that an earlier call on the directory is still running,
// which on a hung network mount can be indefinitely.
var ErrDirBusy = errors.New("a call on the directory is already running")

// ErrDirTimeout reports that the directory did not answer in time.
var ErrDirTimeout = errors.New("the directory did not answer in time")

// DirGuard runs filesystem calls on one directory with a time limit, one at a
// time. A call cannot be canceled (a hung network mount parks it), which is
// why only one may be outstanding: a call while one still runs fails at once
// with ErrDirBusy instead of starting another. The zero value is ready to use.
type DirGuard struct {
	inFlight atomic.Bool
	// read replaces Inspect in tests.
	read func(dir, scratchDir string, now time.Time) Listing
}

// Inspect lists dir as Inspect does, giving up after limit or when ctx ends.
// A read that gives up keeps running in the background until the directory
// answers, and later calls fail with ErrDirBusy until then.
func (g *DirGuard) Inspect(ctx context.Context, dir, scratchDir string, limit time.Duration) (Listing, error) {
	read := g.read
	if read == nil {
		read = Inspect
	}
	return guarded(ctx, g, limit, func() Listing { return read(dir, scratchDir, time.Now()) })
}

// Remove deletes paths, ignoring ones already gone, giving up after limit or
// when ctx ends. It returns the first error a removal reported.
func (g *DirGuard) Remove(ctx context.Context, limit time.Duration, paths ...string) error {
	err, guardErr := guarded(ctx, g, limit, func() error {
		for _, path := range paths {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
	if guardErr != nil {
		return guardErr
	}
	return err
}

func guarded[T any](ctx context.Context, g *DirGuard, limit time.Duration, call func() T) (T, error) {
	var zero T
	if !g.inFlight.CompareAndSwap(false, true) {
		return zero, ErrDirBusy
	}
	result := make(chan T, 1)
	go func() {
		value := call()
		// Free the slot before publishing, so a call made right after this
		// one returns is never refused as busy. result is buffered.
		g.inFlight.Store(false)
		result <- value
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case value := <-result:
		return value, nil
	case <-timer.C:
		return zero, ErrDirTimeout
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

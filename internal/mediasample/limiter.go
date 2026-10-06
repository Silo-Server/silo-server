package mediasample

import (
	"context"
	"sync"
)

// Limiter bounds how many sampling runs a consumer starts at once. It is a
// counting semaphore whose capacity can change while slots are held, plus one
// slot reserved for interactive work (see WithInteractive).
//
// Lowering the capacity lets current holders finish but admits no one new
// until fewer than the new capacity remain, so the limit holds across a
// resize. A channel semaphore cannot be resized in place, and replacing it
// would let holders of the old channel and the new one run side by side.
type Limiter struct {
	mu       sync.Mutex
	capacity int
	inUse    int
	// changed is closed and replaced whenever a slot frees or the capacity
	// grows, waking every waiter to try again.
	changed chan struct{}
	// interactive is the reserved slot. Interactive work would otherwise queue
	// behind every background run waiting for a shared slot.
	interactive chan struct{}
}

// NewLimiter returns a limiter with capacity shared slots, at least one, and
// one interactive slot.
func NewLimiter(capacity int) *Limiter {
	return &Limiter{
		capacity:    max(1, capacity),
		changed:     make(chan struct{}),
		interactive: make(chan struct{}, 1),
	}
}

// Resize changes the number of shared slots, at least one. It applies to new
// acquisitions at once; after a decrease, runs already holding slots finish
// before new ones start.
func (l *Limiter) Resize(capacity int) {
	capacity = max(1, capacity)
	l.mu.Lock()
	defer l.mu.Unlock()
	grew := capacity > l.capacity
	l.capacity = capacity
	if grew {
		l.broadcastLocked()
	}
}

// Acquire waits for a slot and returns its release. Interactive work (see
// WithInteractive) may also take the reserved slot, so it never waits behind
// the background queue for more than one run.
func (l *Limiter) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A nil channel never receives, so background callers only wait on the
	// shared limit.
	var reserved chan struct{}
	if Interactive(ctx) {
		reserved = l.interactive
	}
	for {
		if reserved != nil {
			select {
			case reserved <- struct{}{}:
				return func() { <-reserved }, nil
			default:
			}
		}
		ok, changed := l.tryAcquire()
		if ok {
			return l.release, nil
		}
		select {
		case reserved <- struct{}{}:
			return func() { <-reserved }, nil
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// tryAcquire takes a shared slot if one is free. Otherwise it returns a
// channel that closes when a retry may succeed; reading it under the same lock
// as the check means no wakeup is lost between the two.
func (l *Limiter) tryAcquire() (bool, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inUse < l.capacity {
		l.inUse++
		return true, nil
	}
	return false, l.changed
}

func (l *Limiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inUse--
	l.broadcastLocked()
}

func (l *Limiter) broadcastLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

// interactiveKey marks a context whose sampling a viewer is waiting on.
type interactiveKey struct{}

// WithInteractive marks work a viewer is waiting on, letting it use a
// Limiter's reserved slot. Background callers, such as admin refreshes, must
// not use it or they would queue ahead of playback.
func WithInteractive(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveKey{}, true)
}

// Interactive reports whether ctx was marked by WithInteractive.
func Interactive(ctx context.Context) bool {
	interactive, _ := ctx.Value(interactiveKey{}).(bool)
	return interactive
}

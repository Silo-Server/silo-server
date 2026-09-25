package intromarkers

import "sync"

// slotLimiter is a counting semaphore whose capacity can change while slots
// are held. Lowering the capacity lets current holders finish but admits no
// one new until fewer than the new capacity remain, so the limit holds across
// a resize. A channel semaphore cannot be resized in place, and replacing it
// would let holders of the old channel and the new one run side by side.
type slotLimiter struct {
	mu       sync.Mutex
	capacity int
	inUse    int
	// changed is closed and replaced whenever a slot frees or the capacity
	// grows, waking every waiter to try again.
	changed chan struct{}
}

func newSlotLimiter(capacity int) *slotLimiter {
	return &slotLimiter{capacity: capacity, changed: make(chan struct{})}
}

// tryAcquire takes a slot if one is free. Otherwise it returns a channel that
// closes when a retry may succeed; reading it under the same lock as the
// check means no wakeup is lost between the two.
func (l *slotLimiter) tryAcquire() (bool, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inUse < l.capacity {
		l.inUse++
		return true, nil
	}
	return false, l.changed
}

func (l *slotLimiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inUse--
	l.broadcastLocked()
}

func (l *slotLimiter) resize(capacity int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	grew := capacity > l.capacity
	l.capacity = capacity
	if grew {
		l.broadcastLocked()
	}
}

func (l *slotLimiter) broadcastLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

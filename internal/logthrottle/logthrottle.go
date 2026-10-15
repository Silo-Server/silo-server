// Package logthrottle spaces out a warning that a failure would otherwise
// repeat on every request: at most one per key per interval, each counting
// the ones held back since the last.
package logthrottle

import (
	"sync"
	"time"
)

// maxKeys bounds the keys a Keyed tracks. Keys can come from unauthenticated
// input (a node id a refused request named), so a flood of distinct keys must
// not grow the map without limit.
const maxKeys = 4096

// Keyed throttles warnings per key. The zero value is not usable; call New.
type Keyed[K comparable] struct {
	interval time.Duration
	// Now is the clock; tests replace it before first use.
	Now func() time.Time

	mu      sync.Mutex
	entries map[K]*entry
}

type entry struct {
	last       time.Time
	suppressed int
}

// New returns a throttle that allows one warning per key per interval.
func New[K comparable](interval time.Duration) *Keyed[K] {
	return &Keyed[K]{interval: interval, Now: time.Now, entries: make(map[K]*entry)}
}

// Allow reports whether a warning for key may be logged now. When it may, it
// returns how many were held back for key since the last one it allowed.
//
// With maxKeys keys inside their interval, a new key is held back without
// being tracked: a flood of distinct keys stays quiet instead of growing the
// map, and the keys already tracked keep their own pace.
func (t *Keyed[K]) Allow(key K) (suppressed int, ok bool) {
	now := t.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.entries[key]; e != nil {
		if now.Sub(e.last) < t.interval {
			e.suppressed++
			return 0, false
		}
		suppressed, e.last, e.suppressed = e.suppressed, now, 0
		return suppressed, true
	}
	if len(t.entries) >= maxKeys {
		t.pruneLocked(now)
		if len(t.entries) >= maxKeys {
			return 0, false
		}
	}
	t.entries[key] = &entry{last: now}
	return 0, true
}

// pruneLocked drops the keys whose interval has passed: their next warning is
// allowed either way, at the cost of the count held back for them.
func (t *Keyed[K]) pruneLocked(now time.Time) {
	for key, e := range t.entries {
		if now.Sub(e.last) >= t.interval {
			delete(t.entries, key)
		}
	}
}

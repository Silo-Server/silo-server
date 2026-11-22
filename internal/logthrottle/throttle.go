// Package logthrottle spaces repeated log lines per key.
package logthrottle

import (
	"sync"
	"time"
)

// maxKeys bounds what a Throttle remembers. Its keys can come from an
// unauthenticated header, so a flood of made-up node ids must not grow it
// without limit; past the bound, entries older than the interval are dropped.
const maxKeys = 4096

// Throttle lets one log line per key through per interval. It only limits
// logging: no request depends on it.
type Throttle[K comparable] struct {
	interval time.Duration
	now      func() time.Time

	mu   sync.Mutex
	last map[K]time.Time
	// swept is when the full map was last searched for expired keys.
	swept time.Time
}

// New returns a Throttle that lets one line per key through per interval.
func New[K comparable](interval time.Duration) *Throttle[K] {
	return &Throttle[K]{interval: interval, now: time.Now, last: make(map[K]time.Time)}
}

// SetClock replaces the clock Allow reads. Call it before the first Allow.
func (t *Throttle[K]) SetClock(now func() time.Time) { t.now = now }

// Allow reports whether a line for key may be logged now.
func (t *Throttle[K]) Allow(key K) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
		return false
	}
	if len(t.last) >= maxKeys {
		// A full map is searched for expired keys at most once per interval,
		// so a flood of made-up keys does not pay for a full pass each. Keys
		// not seen yet stay quiet until the next search, which only costs
		// log lines.
		if now.Sub(t.swept) < t.interval {
			return false
		}
		t.swept = now
		for k, last := range t.last {
			if now.Sub(last) >= t.interval {
				delete(t.last, k)
			}
		}
		if len(t.last) >= maxKeys {
			// Every remembered key is recent: a flood. Stay quiet for keys
			// not seen yet rather than grow.
			return false
		}
	}
	t.last[key] = now
	return true
}

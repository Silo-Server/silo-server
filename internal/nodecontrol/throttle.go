package nodecontrol

import (
	"sync"
	"time"
)

// maxThrottleKeys bounds what a logThrottle remembers. Its keys can come from
// an unauthenticated header, so a flood of made-up node ids must not grow it
// without limit; past the bound, entries older than the interval are dropped.
const maxThrottleKeys = 4096

// logThrottle lets one log line per key through per interval. It only limits
// logging: no request depends on it.
type logThrottle[K comparable] struct {
	interval time.Duration
	now      func() time.Time

	mu   sync.Mutex
	last map[K]time.Time
}

func newLogThrottle[K comparable](interval time.Duration) *logThrottle[K] {
	return &logThrottle[K]{interval: interval, now: time.Now, last: make(map[K]time.Time)}
}

// allow reports whether a line for key may be logged now.
func (t *logThrottle[K]) allow(key K) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
		return false
	}
	if len(t.last) >= maxThrottleKeys {
		for k, last := range t.last {
			if now.Sub(last) >= t.interval {
				delete(t.last, k)
			}
		}
		if len(t.last) >= maxThrottleKeys {
			// Every remembered key is recent: a flood. Stay quiet for keys
			// not seen yet rather than grow.
			return false
		}
	}
	t.last[key] = now
	return true
}

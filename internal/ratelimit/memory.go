package ratelimit

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

type memoryEntry struct {
	mu        sync.Mutex // makes checking and taking tokens one step
	perSecond *rate.Limiter
	perMinute *rate.Limiter
	lastUsed  atomic.Int64 // UnixNano timestamp
}

func (e *memoryEntry) touch() {
	e.lastUsed.Store(time.Now().UnixNano())
}

func (e *memoryEntry) lastUsedTime() time.Time {
	return time.Unix(0, e.lastUsed.Load())
}

// MemoryLimiter is an in-memory rate limiter using golang.org/x/time/rate.
type MemoryLimiter struct {
	entries sync.Map
	done    chan struct{}
}

// NewMemoryLimiter creates a new in-memory rate limiter with background eviction.
func NewMemoryLimiter() *MemoryLimiter {
	ml := &MemoryLimiter{done: make(chan struct{})}
	go ml.evictLoop()
	return ml
}

func (ml *MemoryLimiter) Allow(_ context.Context, key string, limit Rate) AllowResult {
	return ml.allowAt(key, limit, time.Now())
}

func (ml *MemoryLimiter) allowAt(key string, limit Rate, now time.Time) AllowResult {
	entry := ml.getOrCreate(key, limit)
	entry.touch()

	// Check both buckets before taking from either, under the entry's lock so
	// the check and the take see the same tokens. Reserving and canceling
	// instead loses tokens when refusals on one key overlap: CancelAt returns
	// only what no later reservation has counted on.
	entry.mu.Lock()
	defer entry.mu.Unlock()

	// A refusal reports the longer wait, so the retry finds both buckets ready.
	if delay := max(tokenDelay(entry.perSecond, now), tokenDelay(entry.perMinute, now)); delay > 0 {
		return AllowResult{
			Allowed:    false,
			RetryAfter: delay,
			Limit:      int(limit.RequestsPerSecond),
			Remaining:  0,
			ResetAt:    roundUpToSecond(now.Add(delay)),
		}
	}

	// Both buckets hold a token, so neither take can fail.
	entry.perSecond.AllowN(now, 1)
	entry.perMinute.AllowN(now, 1)

	remaining := int(math.Max(0, math.Floor(entry.perSecond.TokensAt(now))))
	return AllowResult{
		Allowed:   true,
		Limit:     int(limit.RequestsPerSecond),
		Remaining: remaining,
		ResetAt:   now.Add(time.Second).Truncate(time.Second),
	}
}

// tokenDelay returns how long lim needs to hold a whole token at t, without
// taking one. A bucket that can never hold one reports rate.InfDuration.
func tokenDelay(lim *rate.Limiter, t time.Time) time.Duration {
	missing := 1 - lim.TokensAt(t)
	if missing <= 0 {
		return 0
	}
	perSecond := float64(lim.Limit())
	if perSecond <= 0 || lim.Burst() < 1 {
		return rate.InfDuration
	}
	delay := math.Ceil(missing / perSecond * float64(time.Second))
	if delay >= float64(math.MaxInt64) {
		return rate.InfDuration
	}
	return time.Duration(delay)
}

// roundUpToSecond rounds t up to a whole second. X-RateLimit-Reset carries
// whole seconds, and a floored value would send a client back early. Rounding
// the time, not the delay, keeps a saturated delay from overflowing.
func roundUpToSecond(t time.Time) time.Time {
	if whole := t.Truncate(time.Second); whole.Before(t) {
		return whole.Add(time.Second)
	}
	return t
}

func (ml *MemoryLimiter) getOrCreate(key string, limit Rate) *memoryEntry {
	if val, ok := ml.entries.Load(key); ok {
		return val.(*memoryEntry) //nolint:errcheck,forcetypeassert // entries stores only *memoryEntry
	}
	minuteBurst := int(math.Ceil(limit.RequestsPerMinute))
	if minuteBurst < 1 {
		minuteBurst = 1
	}
	entry := &memoryEntry{
		perSecond: rate.NewLimiter(rate.Limit(limit.RequestsPerSecond), limit.Burst),
		perMinute: rate.NewLimiter(rate.Limit(limit.RequestsPerMinute/60.0), minuteBurst),
	}
	entry.touch()
	actual, _ := ml.entries.LoadOrStore(key, entry)
	return actual.(*memoryEntry) //nolint:errcheck,forcetypeassert // entries stores only *memoryEntry
}

// Clear removes all entries (called on config reload).
func (ml *MemoryLimiter) Clear() {
	ml.entries.Range(func(key, _ any) bool {
		ml.entries.Delete(key)
		return true
	})
}

func (ml *MemoryLimiter) Close() {
	close(ml.done)
}

func (ml *MemoryLimiter) evictLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ml.done:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-10 * time.Minute)
			ml.entries.Range(func(key, value any) bool {
				if value.(*memoryEntry).lastUsedTime().Before(cutoff) { //nolint:errcheck,forcetypeassert // entries stores only *memoryEntry
					ml.entries.Delete(key)
				}
				return true
			})
		}
	}
}

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

	// Check per-second limiter via Reserve
	secRes := entry.perSecond.ReserveN(now, 1)
	if !secRes.OK() {
		return AllowResult{
			Allowed:    false,
			RetryAfter: time.Second,
			Limit:      int(limit.RequestsPerSecond),
			Remaining:  0,
			ResetAt:    now.Add(time.Second).Truncate(time.Second),
		}
	}
	if delay := secRes.DelayFrom(now); delay > 0 {
		secRes.CancelAt(now)
		resetAt := now.Add(time.Second).Truncate(time.Second)
		// An empty per-minute bucket would refuse the retry too, so wait for both.
		if minDelay := tokenDelay(entry.perMinute, now); minDelay > delay {
			delay = minDelay
			// Round up: X-RateLimit-Reset carries whole seconds. Rounding the
			// time, not the delay, keeps a saturated delay from overflowing.
			resetAt = now.Add(delay)
			if whole := resetAt.Truncate(time.Second); whole.Before(resetAt) {
				resetAt = whole.Add(time.Second)
			}
		}
		return AllowResult{
			Allowed:    false,
			RetryAfter: delay,
			Limit:      int(limit.RequestsPerSecond),
			Remaining:  0,
			ResetAt:    resetAt,
		}
	}

	// Check per-minute limiter via Reserve
	minRes := entry.perMinute.ReserveN(now, 1)
	if !minRes.OK() {
		secRes.CancelAt(now) // return the per-second token
		return AllowResult{
			Allowed:    false,
			RetryAfter: time.Minute,
			Limit:      int(limit.RequestsPerSecond),
			Remaining:  0,
			ResetAt:    now.Add(time.Minute).Truncate(time.Minute),
		}
	}
	if delay := minRes.DelayFrom(now); delay > 0 {
		secRes.CancelAt(now) // return the per-second token
		minRes.CancelAt(now) // return the per-minute token
		return AllowResult{
			Allowed:    false,
			RetryAfter: delay,
			Limit:      int(limit.RequestsPerSecond),
			Remaining:  0,
			ResetAt:    now.Add(delay),
		}
	}

	remaining := int(math.Max(0, math.Floor(entry.perSecond.TokensAt(now))))
	return AllowResult{
		Allowed:   true,
		Limit:     int(limit.RequestsPerSecond),
		Remaining: remaining,
		ResetAt:   now.Add(time.Second).Truncate(time.Second),
	}
}

// tokenDelay returns how long lim needs to hold a whole token at t, without
// taking one.
func tokenDelay(lim *rate.Limiter, t time.Time) time.Duration {
	missing := 1 - lim.TokensAt(t)
	perSecond := float64(lim.Limit())
	if missing <= 0 || perSecond <= 0 {
		return 0
	}
	delay := math.Ceil(missing / perSecond * float64(time.Second))
	if delay >= float64(math.MaxInt64) {
		return rate.InfDuration
	}
	return time.Duration(delay)
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

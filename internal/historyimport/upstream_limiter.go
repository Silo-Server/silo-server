package historyimport

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	historyImportUpstreamRequestsPerSecond = 2
	historyImportUpstreamBurst             = 1
	// maxTrackedUpstreamHosts caps per-host buckets so user-supplied addresses
	// cannot grow the process-wide map without bound.
	maxTrackedUpstreamHosts = 256
	// minIdleBucketEvictWindow keeps a freshly inserted bucket from being
	// swept before its caller enters Wait, and throttles saturated sweeps.
	minIdleBucketEvictWindow = time.Second
)

var sharedHistoryImportUpstreamLimiter = newUpstreamRateLimiter(
	rate.Limit(historyImportUpstreamRequestsPerSecond),
	historyImportUpstreamBurst,
)

type trackedHostLimiter struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

type upstreamRateLimiter struct {
	mu             sync.Mutex
	limit          rate.Limit
	burst          int
	maxHosts       int
	limiters       map[string]*trackedHostLimiter
	overflow       *rate.Limiter
	lastEmptySweep time.Time
}

func newUpstreamRateLimiter(limit rate.Limit, burst int) *upstreamRateLimiter {
	if burst <= 0 {
		burst = 1
	}
	return &upstreamRateLimiter{
		limit:    limit,
		burst:    burst,
		maxHosts: maxTrackedUpstreamHosts,
		limiters: make(map[string]*trackedHostLimiter),
		overflow: rate.NewLimiter(limit, burst),
	}
}

func (l *upstreamRateLimiter) Wait(ctx context.Context, reqURL *url.URL) error {
	if l == nil || reqURL == nil {
		return nil
	}
	key := limiterKey(reqURL)
	if key == "" {
		return nil
	}

	return l.limiterFor(key).Wait(ctx)
}

func (l *upstreamRateLimiter) idleEvictWindow() time.Duration {
	if l.limit > 0 && l.limit != rate.Inf {
		refill := time.Duration(float64(time.Second) / float64(l.limit))
		if refill > minIdleBucketEvictWindow {
			return refill
		}
	}
	return minIdleBucketEvictWindow
}

func (l *upstreamRateLimiter) limiterFor(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if l.limiters == nil {
		l.limiters = make(map[string]*trackedHostLimiter)
	}
	if entry, ok := l.limiters[key]; ok {
		entry.lastUsed = now
		return entry.limiter
	}
	maxHosts := l.maxHosts
	if maxHosts <= 0 {
		maxHosts = maxTrackedUpstreamHosts
	}
	window := l.idleEvictWindow()
	if len(l.limiters) >= maxHosts && (l.lastEmptySweep.IsZero() || now.Sub(l.lastEmptySweep) >= window) {
		// Evict only buckets that have been idle past window and are fully
		// replenished, so freshly created buckets and active waiters are never
		// dropped.
		full := float64(l.burst)
		evicted := false
		for host, entry := range l.limiters {
			if now.Sub(entry.lastUsed) >= window && entry.limiter.TokensAt(now) >= full {
				delete(l.limiters, host)
				evicted = true
			}
		}
		if evicted {
			l.lastEmptySweep = time.Time{}
		} else {
			l.lastEmptySweep = now
		}
	}
	if len(l.limiters) >= maxHosts {
		if l.overflow == nil {
			l.overflow = rate.NewLimiter(l.limit, l.burst)
		}
		return l.overflow
	}
	limiter := rate.NewLimiter(l.limit, l.burst)
	l.limiters[key] = &trackedHostLimiter{limiter: limiter, lastUsed: now}
	return limiter
}

func limiterKey(reqURL *url.URL) string {
	if reqURL == nil {
		return ""
	}
	return strings.ToLower(reqURL.Host)
}

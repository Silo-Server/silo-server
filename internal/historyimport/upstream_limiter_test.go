package historyimport

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestUpstreamRateLimiterBoundsTrackedHostsAndKeepsActiveBuckets(t *testing.T) {
	t.Parallel()

	limiter := newUpstreamRateLimiter(rate.Limit(0.01), 1)
	limiter.maxHosts = 2

	a := limiter.limiterFor("a.example.test")
	b := limiter.limiterFor("b.example.test")
	if !a.Allow() || !b.Allow() {
		t.Fatal("expected initial burst tokens on tracked hosts")
	}

	// Both tracked buckets are active (tokens < burst), so a third host falls
	// back to the shared overflow limiter without evicting either active bucket.
	c := limiter.limiterFor("c.example.test")
	if c != limiter.overflow {
		t.Fatal("expected overflow limiter while all tracked buckets are active")
	}
	if got := len(limiter.limiters); got != 2 {
		t.Fatalf("tracked hosts = %d, want 2", got)
	}
	if limiter.limiterFor("a.example.test") != a || limiter.limiterFor("b.example.test") != b {
		t.Fatal("active host buckets were evicted")
	}

	// A freshly created full-token bucket is not evicted before its caller enters Wait.
	idle := newUpstreamRateLimiter(rate.Inf, 1)
	idle.maxHosts = 2
	one := idle.limiterFor("one.example.test")
	_ = idle.limiterFor("two.example.test")
	if got := idle.limiterFor("fresh.example.test"); got != idle.overflow {
		t.Fatal("freshly inserted full-token bucket was evicted before the idle window elapsed")
	}
	if idle.limiterFor("one.example.test") != one {
		t.Fatal("freshly inserted bucket did not remain tracked")
	}

	// Once a full-token bucket ages past the idle window, sweeping evicts it to admit a new host.
	idle.mu.Lock()
	idle.limiters["one.example.test"].lastUsed = time.Now().Add(-2 * idle.idleEvictWindow())
	idle.lastEmptySweep = time.Time{}
	idle.mu.Unlock()

	three := idle.limiterFor("three.example.test")
	if three == idle.overflow {
		t.Fatal("expected aged idle bucket sweep to free a slot for three.example.test")
	}
	if got := len(idle.limiters); got > 2 {
		t.Fatalf("tracked hosts = %d, want <= 2", got)
	}
}

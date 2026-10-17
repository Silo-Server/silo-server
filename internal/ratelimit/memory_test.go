package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestMemoryLimiterRetryAfterCoversEmptyMinuteBucket(t *testing.T) {
	ml := NewMemoryLimiter()
	t.Cleanup(ml.Close)

	limit := Rate{RequestsPerSecond: 1, RequestsPerMinute: 2, Burst: 2}
	now := time.Unix(1_800_000_000, 0)

	for i := range 2 {
		if res := ml.allowAt("client", limit, now); !res.Allowed {
			t.Fatalf("request %d refused, want allowed", i+1)
		}
	}

	// Both buckets are now empty. Refuse a few retries at the same instant to
	// show a refused request takes no tokens.
	var refused AllowResult
	for range 3 {
		refused = ml.allowAt("client", limit, now)
		if refused.Allowed {
			t.Fatal("request allowed with both buckets empty")
		}
	}
	if refused.RetryAfter < 30*time.Second {
		t.Fatalf("retry after = %s, want at least the minute bucket's 30s", refused.RetryAfter)
	}
	if res := ml.allowAt("client", limit, now.Add(refused.RetryAfter)); !res.Allowed {
		t.Fatalf("request after retry after refused again with retry after %s", res.RetryAfter)
	}

	// X-RateLimit-Reset carries whole seconds; a client waiting for it from
	// mid-second must get through too.
	ml.Clear()
	now = now.Add(400 * time.Millisecond)
	for range 3 {
		refused = ml.allowAt("client", limit, now)
	}
	if refused.Allowed {
		t.Fatal("request allowed with both buckets empty")
	}
	reset := time.Unix(refused.ResetAt.Unix(), 0)
	if reset.Before(now.Add(refused.RetryAfter)) {
		t.Fatalf("reset at %s is before %s", reset, now.Add(refused.RetryAfter))
	}
	if res := ml.allowAt("client", limit, reset); !res.Allowed {
		t.Fatalf("request at reset refused again with retry after %s", res.RetryAfter)
	}
}

func TestMemoryLimiterRetryAfterKeepsSecondWaitWhenMinuteHasRoom(t *testing.T) {
	ml := NewMemoryLimiter()
	t.Cleanup(ml.Close)

	limit := Rate{RequestsPerSecond: 1, RequestsPerMinute: 60, Burst: 1}
	now := time.Unix(1_800_000_000, 0)

	if res := ml.allowAt("client", limit, now); !res.Allowed {
		t.Fatal("first request refused, want allowed")
	}
	refused := ml.allowAt("client", limit, now)
	if refused.Allowed {
		t.Fatal("second request allowed, want refused by the per-second bucket")
	}
	if refused.RetryAfter != time.Second {
		t.Fatalf("retry after = %s, want 1s", refused.RetryAfter)
	}
	if res := ml.allowAt("client", limit, now.Add(refused.RetryAfter)); !res.Allowed {
		t.Fatalf("request after retry after refused again with retry after %s", res.RetryAfter)
	}
}

func TestMemoryLimiterRetryAfterSaturatesForTinyMinuteRate(t *testing.T) {
	ml := NewMemoryLimiter()
	t.Cleanup(ml.Close)

	// A minute bucket this slow needs longer than a Duration can hold.
	limit := Rate{RequestsPerSecond: 1, RequestsPerMinute: 1e-12, Burst: 1}
	now := time.Unix(1_800_000_000, 0).Add(400 * time.Millisecond)

	if res := ml.allowAt("client", limit, now); !res.Allowed {
		t.Fatal("first request refused, want allowed")
	}
	refused := ml.allowAt("client", limit, now)
	if refused.Allowed {
		t.Fatal("second request allowed, want refused")
	}
	if refused.RetryAfter != rate.InfDuration {
		t.Fatalf("retry after = %s, want rate.InfDuration", refused.RetryAfter)
	}
	if refused.ResetAt.Before(now) {
		t.Fatalf("reset at %s is before now %s", refused.ResetAt, now)
	}
}

// Refusals that overlap on one key must not take tokens. Reserve-and-cancel
// did: CancelAt returns only what no later reservation has counted on, so each
// overlap left the bucket a token lower and stretched the wait past the rate.
func TestMemoryLimiterConcurrentRefusalsTakeNoTokens(t *testing.T) {
	ml := NewMemoryLimiter()
	t.Cleanup(ml.Close)

	limit := Rate{RequestsPerSecond: 1, RequestsPerMinute: 1000, Burst: 1}
	now := time.Unix(1_800_000_000, 0)

	if res := ml.allowAt("client", limit, now); !res.Allowed {
		t.Fatal("first request refused, want allowed")
	}

	const workers = 200
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start
			for range 5 {
				if res := ml.allowAt("client", limit, now); res.Allowed {
					t.Error("request allowed with the per-second bucket empty")
				}
			}
		})
	}
	close(start)
	wg.Wait()

	refused := ml.allowAt("client", limit, now)
	if refused.Allowed {
		t.Fatal("request allowed with the per-second bucket empty")
	}
	if refused.RetryAfter != time.Second {
		t.Fatalf("retry after = %s, want 1s: refused requests took tokens", refused.RetryAfter)
	}
	if res := ml.allowAt("client", limit, now.Add(time.Second)); !res.Allowed {
		t.Fatalf("request after 1s refused with retry after %s", res.RetryAfter)
	}
}

// Concurrent requests on one key must not be allowed past the burst.
func TestMemoryLimiterConcurrentRequestsStayWithinBurst(t *testing.T) {
	ml := NewMemoryLimiter()
	t.Cleanup(ml.Close)

	limit := Rate{RequestsPerSecond: 10, RequestsPerMinute: 600, Burst: 10}
	now := time.Unix(1_800_000_000, 0)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		wg.Go(func() {
			<-start
			if ml.allowAt("client", limit, now).Allowed {
				allowed.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != int64(limit.Burst) {
		t.Fatalf("allowed %d concurrent requests, want the burst of %d", got, limit.Burst)
	}
}

// X-RateLimit-Reset must not come before Retry-After, or a client that waits
// for it is refused again. Sign-in limits refill the per-second bucket slower
// than one a second, so their per-second wait spans several seconds.
func TestMemoryLimiterResetCoversMultiSecondWait(t *testing.T) {
	ml := NewMemoryLimiter()
	t.Cleanup(ml.Close)

	limit := Rate{RequestsPerSecond: 20.0 / 60, RequestsPerMinute: 20, Burst: 10}
	now := time.Unix(1_800_000_000, 0).Add(300 * time.Millisecond)

	for i := range limit.Burst {
		if res := ml.allowAt("client", limit, now); !res.Allowed {
			t.Fatalf("request %d refused, want allowed", i+1)
		}
	}
	refused := ml.allowAt("client", limit, now)
	if refused.Allowed {
		t.Fatal("request past the burst allowed")
	}
	if refused.RetryAfter < 2*time.Second {
		t.Fatalf("retry after = %s, want the per-second bucket's 3s", refused.RetryAfter)
	}
	reset := time.Unix(refused.ResetAt.Unix(), 0)
	if reset.Before(now.Add(refused.RetryAfter)) {
		t.Fatalf("reset at %s is before %s", reset, now.Add(refused.RetryAfter))
	}
	if res := ml.allowAt("client", limit, reset); !res.Allowed {
		t.Fatalf("request at reset refused again with retry after %s", res.RetryAfter)
	}
}

// When only the minute bucket is empty, the reset rounds up too.
func TestMemoryLimiterResetCoversMinuteWait(t *testing.T) {
	ml := NewMemoryLimiter()
	t.Cleanup(ml.Close)

	limit := Rate{RequestsPerSecond: 10, RequestsPerMinute: 10, Burst: 10}
	now := time.Unix(1_800_000_000, 0).Add(300 * time.Millisecond)

	for i := range 10 {
		if res := ml.allowAt("client", limit, now.Add(time.Duration(i)*10*time.Millisecond)); !res.Allowed {
			t.Fatalf("request %d refused, want allowed", i+1)
		}
	}
	now = now.Add(100 * time.Millisecond)
	refused := ml.allowAt("client", limit, now)
	if refused.Allowed {
		t.Fatal("request with the minute bucket empty allowed")
	}
	reset := time.Unix(refused.ResetAt.Unix(), 0)
	if reset.Before(now.Add(refused.RetryAfter)) {
		t.Fatalf("reset at %s is before %s", reset, now.Add(refused.RetryAfter))
	}
	if res := ml.allowAt("client", limit, reset); !res.Allowed {
		t.Fatalf("request at reset refused again with retry after %s", res.RetryAfter)
	}
}

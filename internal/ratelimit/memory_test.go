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

// X-RateLimit-Reset carries whole seconds and must not send a client back
// before the wait it reports, whichever bucket is empty.
func TestMemoryLimiterResetCoversWait(t *testing.T) {
	start := time.Unix(1_800_000_000, 0).Add(300 * time.Millisecond)
	tests := []struct {
		name    string
		limit   Rate
		spacing time.Duration // between the requests that empty the bucket
		refuse  time.Duration // after start, when the refused request arrives
	}{
		// Sign-in limits refill below one token a second.
		{"multi-second per-second wait", Rate{RequestsPerSecond: 20.0 / 60, RequestsPerMinute: 20, Burst: 10}, 0, 0},
		{"minute bucket empty", Rate{RequestsPerSecond: 10, RequestsPerMinute: 10, Burst: 10}, 10 * time.Millisecond, 100 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ml := NewMemoryLimiter()
			t.Cleanup(ml.Close)

			for i := range tt.limit.Burst {
				if res := ml.allowAt("client", tt.limit, start.Add(time.Duration(i)*tt.spacing)); !res.Allowed {
					t.Fatalf("request %d refused, want allowed", i+1)
				}
			}
			now := start.Add(tt.refuse)
			refused := ml.allowAt("client", tt.limit, now)
			if refused.Allowed {
				t.Fatal("request past the limit allowed")
			}
			if refused.RetryAfter <= time.Second {
				t.Fatalf("retry after = %s, want a multi-second wait", refused.RetryAfter)
			}
			reset := time.Unix(refused.ResetAt.Unix(), 0)
			if reset.Before(now.Add(refused.RetryAfter)) {
				t.Fatalf("reset at %s is before %s", reset, now.Add(refused.RetryAfter))
			}
			if res := ml.allowAt("client", tt.limit, reset); !res.Allowed {
				t.Fatalf("request at reset refused again with retry after %s", res.RetryAfter)
			}
		})
	}
}

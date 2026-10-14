package ratelimit

import (
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

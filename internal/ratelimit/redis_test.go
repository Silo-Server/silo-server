package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisWindowLimitsHonorBurst(t *testing.T) {
	sec, minute, effective := redisWindowLimits(Rate{
		RequestsPerSecond: 1,
		RequestsPerMinute: 60,
		Burst:             30,
	})
	if sec != 30 || minute != 60 || effective != 30 {
		t.Fatalf("limits = (%d, %d, %d), want (30, 60, 30)", sec, minute, effective)
	}
}

func TestRedisWindowLimitsKeepMinuteOnlyRatesUsable(t *testing.T) {
	sec, minute, _ := redisWindowLimits(Rate{
		RequestsPerSecond: 10.0 / 60.0,
		RequestsPerMinute: 10,
		Burst:             6,
	})
	if sec != 6 || minute != 10 {
		t.Fatalf("limits = (%d, %d), want burst 6 and minute 10", sec, minute)
	}
}

func TestRedisRetryAfterWaitsForAFullMinuteWindow(t *testing.T) {
	minuteStart := time.Unix(1_800_000_000-1_800_000_000%60, 0)
	const secTTL = 2 * time.Second

	tests := []struct {
		name     string
		second   int
		minCount int
		want     time.Duration
	}{
		{name: "minute window has room", second: 10, minCount: 20, want: secTTL},
		{name: "minute window full", second: 10, minCount: 30, want: 50 * time.Second},
		// The second key's cleanup TTL outlives the window; both roll over at the minute.
		{name: "minute window full in its last second", second: 59, minCount: 30, want: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := minuteStart.Add(time.Duration(tt.second) * time.Second)
			got := redisRetryAfter(now, tt.minCount, 30, secTTL)
			if got != tt.want {
				t.Fatalf("retry after = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRedisLimiterRetryAfterWhenBothWindowsFull(t *testing.T) {
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	limiter := NewRedisLimiter(client)
	// Burst 10, 10 a minute: one burst fills both windows in the same second.
	rate := Rate{RequestsPerSecond: 10.0 / 60.0, RequestsPerMinute: 10, Burst: 10}
	ctx := context.WithoutCancel(t.Context())

	// A burst that crosses a minute boundary lands in two minute windows, so
	// try again with a fresh key when that happens.
	for attempt := 0; attempt < 3; attempt++ {
		key := "test:" + uuid.NewString()
		t.Cleanup(func() {
			if keys, _ := client.Keys(ctx, "silo:ratelimit:"+key+":*").Result(); len(keys) > 0 {
				_ = client.Del(ctx, keys...).Err()
			}
		})
		startMinute := time.Now().Unix() / 60
		var refused AllowResult
		for range rate.Burst + 1 {
			if refused = limiter.Allow(ctx, key, rate); !refused.Allowed {
				break
			}
		}
		now := time.Now()
		if now.Unix()/60 != startMinute {
			continue
		}
		if refused.Allowed {
			t.Fatal("limiter never refused a request")
		}
		untilMinute := time.Duration(60-now.Unix()%60) * time.Second
		if refused.RetryAfter < untilMinute {
			t.Fatalf("retry after = %s, want the minute window's remaining %s", refused.RetryAfter, untilMinute)
		}
		return
	}
	t.Fatal("every attempt crossed a minute boundary")
}

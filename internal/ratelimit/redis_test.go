package ratelimit

import (
	"testing"
	"time"
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

package clientclock

import (
	"testing"
	"time"
)

func TestClamp(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		client time.Time
		want   time.Time
	}{
		{"zero defaults to now", time.Time{}, now},
		{"far future clamps to now", now.Add(48 * time.Hour), now},
		{"just past skew clamps to now", now.Add(MaxSkew + time.Minute), now},
		{"within skew is kept", now.Add(time.Minute), now.Add(time.Minute)},
		{"past is kept", now.Add(-3 * time.Hour), now.Add(-3 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Clamp(tc.client, now); !got.Equal(tc.want) {
				t.Fatalf("Clamp(%v) = %v, want %v", tc.client, got, tc.want)
			}
		})
	}
}

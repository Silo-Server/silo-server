package handlers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestClampEventAt(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		client time.Time
		want   time.Time
	}{
		{"zero defaults to now", time.Time{}, now},
		{"far future clamps to now", now.Add(48 * time.Hour), now},
		{"just past skew clamps to now", now.Add(progressClockSkew + time.Minute), now},
		{"within skew is kept", now.Add(time.Minute), now.Add(time.Minute)},
		{"past is kept", now.Add(-3 * time.Hour), now.Add(-3 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampEventAt(tc.client, now); !got.Equal(tc.want) {
				t.Fatalf("clampEventAt(%v) = %v, want %v", tc.client, got, tc.want)
			}
		})
	}
}

func TestOfflineProgressState(t *testing.T) {
	thresholds := userstore.ProgressThresholds{WatchedPct: 90, MinResumePct: 5}

	// Below the min-resume floor → skipped.
	if _, _, skip := userstore.ResolveProgressState(10, 1000, thresholds); !skip {
		t.Fatal("tiny progress should be skipped")
	}
	// Above the watched threshold → completed latch, position reset.
	pos, completed, skip := userstore.ResolveProgressState(950, 1000, thresholds)
	if skip || !completed || pos != 0 {
		t.Fatalf("watched item = (pos=%v completed=%v skip=%v), want (0 true false)", pos, completed, skip)
	}
	// Normal mid-progress → kept as-is.
	pos, completed, skip = userstore.ResolveProgressState(500, 1000, thresholds)
	if skip || completed || pos != 500 {
		t.Fatalf("mid progress = (pos=%v completed=%v skip=%v), want (500 false false)", pos, completed, skip)
	}
}

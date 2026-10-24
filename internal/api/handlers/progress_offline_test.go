package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

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

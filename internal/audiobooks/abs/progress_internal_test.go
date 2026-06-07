package abs

import (
	"testing"
	"time"
)

func TestProgressRowToABSEmitsDuration(t *testing.T) {
	out := progressRowToABS(ProgressRow{
		UserID:          "u1",
		ContentID:       "b1",
		CurrentSeconds:  30,
		DurationSeconds: 3600,
		ProgressPct:     0.0083,
		UpdatedAt:       time.Now(),
	})
	if out["duration"] != float64(3600) {
		t.Errorf("duration = %v, want 3600", out["duration"])
	}
}

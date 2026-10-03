package keyframes

import (
	"slices"
	"testing"
)

func TestPlanSegments(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keyframes []float64
		want      []float64
	}{
		{name: "no keyframes", keyframes: nil, want: nil},
		{name: "one keyframe", keyframes: []float64{0}, want: []float64{0}},
		// Regular 1s keyframes: one segment every 2s.
		{name: "regular", keyframes: []float64{0, 1, 2, 3, 4, 5, 6}, want: []float64{0, 2, 4, 6}},
		// A keyframe after the target ends the segment there.
		{name: "late keyframe", keyframes: []float64{0, 1.3, 2.6, 3.9, 5.2}, want: []float64{0, 2.6, 5.2}},
		// After a long gap the targets lag behind, and the next keyframes each
		// start a segment until they catch up, as FFmpeg's muxer does.
		{name: "catching up", keyframes: []float64{0, 7.0, 7.4, 7.8, 9.9}, want: []float64{0, 7.0, 7.4, 7.8, 9.9}},
		// Targets count from the first keyframe, where FFmpeg's output starts.
		{name: "late start", keyframes: []float64{0.5, 1.5, 2.5, 3.5}, want: []float64{0.5, 2.5}},
		// A keyframe exactly on a target cuts there, though a nonzero origin
		// leaves float error in the subtraction.
		{name: "exact targets after a nonzero origin", keyframes: []float64{0.28, 2.28, 4.28, 6.28, 8.28}, want: []float64{0.28, 2.28, 4.28, 6.28, 8.28}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanSegments(tc.keyframes, 2); !slices.Equal(got, tc.want) {
				t.Fatalf("PlanSegments = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSegmentDurations(t *testing.T) {
	got := SegmentDurations([]float64{0, 2.6, 5.2}, 6)
	want := []float64{2.6, 2.6, 0.8}
	for i := range want {
		if diff := got[i] - want[i]; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("SegmentDurations = %v, want %v", got, want)
		}
	}
	if got := SegmentDurations([]float64{0, 5}, 4); got[1] != 0 {
		t.Fatalf("last duration past the end = %v, want 0", got[1])
	}
}

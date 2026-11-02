package extrastills

import "testing"

func TestFrameSeconds(t *testing.T) {
	for _, tt := range []struct {
		duration int
		want     float64
	}{
		{0, 0},     // unknown: the first frame
		{4, 2},     // shorter than the floor: the middle
		{5, 2.5},   // the floor would be the last frame
		{30, 5},    // a tenth is under the 5s floor
		{300, 30},  // a tenth of the way in
		{3600, 60}, // a minute at most
	} {
		if got := frameSeconds(tt.duration); got != tt.want {
			t.Errorf("frameSeconds(%d) = %v, want %v", tt.duration, got, tt.want)
		}
	}
}

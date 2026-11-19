package intromarkers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// stats builds a keyframe: PBlack at luma 20/26/32, then luma min, low,
// average, high, max, and saturation low, average, high, max.
func stats(pb20, pb26, pb32 uint8, y [5]float32, sat [4]float32) mediasample.FrameStats {
	return mediasample.FrameStats{
		PBlack: []uint8{pb20, pb26, pb32},
		YMin:   y[0], YLow: y[1], YAvg: y[2], YHigh: y[3], YMax: y[4],
		SatLow: sat[0], SatAvg: sat[1], SatHigh: sat[2], SatMax: sat[3],
	}
}

// keyframes builds classified keyframes from "seconds class" pairs: c
// content, b black, L lettered, C card.
func keyframes(t *testing.T, spec string) []creditsKeyframe {
	t.Helper()
	parsed, err := parseKeyframes(spec)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

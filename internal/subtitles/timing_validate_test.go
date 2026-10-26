package subtitles

import (
	"errors"
	"math"
	"testing"
)

func TestValidateTiming(t *testing.T) {
	valid := []Timing{{}, {Scale: 1}, {OffsetMS: MaxTimingOffsetMS}, {OffsetMS: -MaxTimingOffsetMS}, {Scale: 0.9}, {Scale: 1.1}, {Scale: 25.0 / 23.976, OffsetMS: -250}}
	for _, timing := range valid {
		if err := ValidateTiming(timing); err != nil {
			t.Errorf("ValidateTiming(%+v) = %v", timing, err)
		}
	}
	invalid := []Timing{{OffsetMS: MaxTimingOffsetMS + 1}, {OffsetMS: -MaxTimingOffsetMS - 1}, {Scale: 0.89}, {Scale: 1.11}, {Scale: -1}, {Scale: math.NaN()}, {Scale: math.Inf(1)}}
	for _, timing := range invalid {
		if err := ValidateTiming(timing); !errors.Is(err, ErrInvalidTiming) {
			t.Errorf("ValidateTiming(%+v) = %v, want ErrInvalidTiming", timing, err)
		}
	}
}

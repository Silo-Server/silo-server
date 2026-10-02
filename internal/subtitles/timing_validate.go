package subtitles

import (
	"errors"
	"fmt"
	"math"
)

// Stored timing bounds. They match the downloaded_subtitles CHECK constraints.
const (
	MaxTimingOffsetMS = 600000
	MinTimingScale    = 0.9
	MaxTimingScale    = 1.1
)

var (
	// ErrInvalidTiming reports a timing correction outside the stored bounds.
	ErrInvalidTiming = errors.New("invalid subtitle timing")
	// ErrTimingUnsupported reports a timing correction on a subtitle format
	// that delivery cannot retime.
	ErrTimingUnsupported = errors.New("subtitle format does not support timing correction")
)

// ValidateTiming checks t against the bounds a stored subtitle row accepts:
// |OffsetMS| ≤ MaxTimingOffsetMS and a finite normalized Scale within
// [MinTimingScale, MaxTimingScale]. Errors wrap ErrInvalidTiming.
func ValidateTiming(t Timing) error {
	if t.OffsetMS < -MaxTimingOffsetMS || t.OffsetMS > MaxTimingOffsetMS {
		return fmt.Errorf("%w: offset %d ms is outside ±%d ms", ErrInvalidTiming, t.OffsetMS, MaxTimingOffsetMS)
	}
	scale := t.Normalized().Scale
	if math.IsNaN(scale) || math.IsInf(scale, 0) || scale < MinTimingScale || scale > MaxTimingScale {
		return fmt.Errorf("%w: scale %v is outside [%v, %v]", ErrInvalidTiming, t.Scale, MinTimingScale, MaxTimingScale)
	}
	return nil
}

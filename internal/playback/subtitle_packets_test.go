package playback

import (
	"testing"
	"time"
)

func TestParseFFprobeSeconds(t *testing.T) {
	tests := map[string]time.Duration{
		"":            0,
		"N/A":         0,
		"0.000000":    0,
		"1.500000":    1500 * time.Millisecond,
		"-0.033000":   -33 * time.Millisecond,
		"12":          12 * time.Second,
		"3.1415926":   3141592 * time.Microsecond,
		"7200.000001": 2*time.Hour + time.Microsecond,
	}
	for in, want := range tests {
		got, err := parseFFprobeSeconds(in)
		if err != nil || got != want {
			t.Errorf("%q: %v, %v; want %v", in, got, err, want)
		}
		// FFmpeg takes the offset back at the same precision.
		if back, err := parseFFprobeSeconds(formatMicroseconds(got)); err != nil || back != got {
			t.Errorf("%q: %s reads back as %v", in, formatMicroseconds(got), back)
		}
	}
	if _, err := parseFFprobeSeconds("1.5x"); err == nil {
		t.Error("parsed a malformed time")
	}
}

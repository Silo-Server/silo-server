package playback

import (
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
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

// A conversion whose packets were refused before FFmpeg got any reports the
// refusal, not only FFmpeg's failure on empty input, so callers can tell
// that the file, not the conversion, is at fault.
func TestConversionReportsARefusedWrite(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	in := &SubtitlePacketInput{StreamFormat: "matroska", Stream: func() (io.ReadCloser, error) {
		return nil, embeddedsub.ErrTooLarge
	}}
	if _, err := ExtractSubtitlePacketsWebVTT(t.Context(), in, ffmpeg); !errors.Is(err, embeddedsub.ErrTooLarge) {
		t.Fatalf("err = %v, want it to carry ErrTooLarge", err)
	}
}

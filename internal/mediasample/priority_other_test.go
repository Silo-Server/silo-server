//go:build !linux

package mediasample

import (
	"context"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// Background runs start like any other run where priority is not lowered.
func TestRunBackgroundPriority(t *testing.T) {
	ffmpeg := writeScript(t, `echo "[silencedetect @ 0x1] silence_start: 2" >&2`)
	runner := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}
	req := Request{
		Input:  "/media/a.mkv",
		Window: &Window{StartSeconds: 10, DurationSeconds: 20},
		Audio:  &AudioOutput{Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}},
	}

	foreground, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("foreground Run: %v", err)
	}
	req.Background = true
	background, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("background Run: %v", err)
	}
	if !reflect.DeepEqual(background, foreground) {
		t.Fatalf("background result %+v, want %+v", background, foreground)
	}
	if want := []Interval{{Start: 12}}; !reflect.DeepEqual(background.Silences, want) {
		t.Fatalf("silences %+v, want %+v", background.Silences, want)
	}
}

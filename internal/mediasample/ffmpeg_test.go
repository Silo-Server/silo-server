package mediasample

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// realFFmpeg returns an ffmpeg from PATH and its capabilities, or skips.
func realFFmpeg(t *testing.T) (string, Capabilities) {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	caps, err := LoadCapabilities(ctx, path)
	if err != nil {
		t.Skipf("ffmpeg capabilities unavailable: %v", err)
	}
	return path, caps
}

// TestRunWithRealFFmpeg samples a generated clip: tone, silence from 4 s to
// 6 s, tone.
func TestRunWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	silence := Request{Audio: &AudioOutput{Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}}}
	if err := caps.Require(silence); err != nil {
		t.Skipf("ffmpeg cannot detect silence: %v", err)
	}
	clip := filepath.Join(t.TempDir(), "clip.wav")
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "aevalsrc=if(between(t\\,4\\,6)\\,0\\,0.5*sin(2*PI*440*t)):d=12:s=44100", clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate clip: %v: %s", err, output)
	}
	runner := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	silence.Input = clip
	silence.Window = &Window{StartSeconds: 2, DurationSeconds: 8}
	result, err := runner.Run(ctx, silence)
	if err != nil {
		t.Fatalf("silence run: %v", err)
	}
	if len(result.Silences) != 1 ||
		math.Abs(result.Silences[0].Start-4) > 0.05 || math.Abs(result.Silences[0].End-6) > 0.05 {
		t.Fatalf("silences %+v, want one from 4 s to 6 s in media time", result.Silences)
	}

	fingerprint := Request{Input: clip, Window: &Window{DurationSeconds: 12}, Audio: &AudioOutput{Fingerprint: true}, Threads: 1}
	if err := caps.Require(fingerprint); err != nil {
		t.Logf("skipping the fingerprint run: %v", err)
		return
	}
	result, err = runner.Run(ctx, fingerprint)
	if err != nil {
		t.Fatalf("fingerprint run: %v", err)
	}
	if len(result.Fingerprint) == 0 {
		t.Fatal("fingerprint run returned no points")
	}
}

// TestRunStatsWithRealFFmpeg samples keyframes of a generated clip, one per
// second, with a white bar on black, together with its audio in one run.
func TestRunStatsWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	req := Request{
		Window: &Window{StartSeconds: 2, DurationSeconds: 8, KeyframesOnly: true},
		Audio:  &AudioOutput{Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}},
		Stats:  &StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 480, BlackThresholds: []int{20, 26, 32}},
	}
	if err := caps.Require(req); err != nil {
		t.Skipf("ffmpeg cannot sample frame statistics: %v", err)
	}
	if !caps.HasFilter("drawbox") || !caps.HasFilter("aevalsrc") {
		t.Skip("ffmpeg lacks the filters that generate the clip")
	}
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=640x360:r=24:d=12,drawbox=x=200:y=170:w=240:h=20:color=white:t=fill",
		"-f", "lavfi", "-i", "aevalsrc=if(between(t\\,4\\,6)\\,0\\,0.5*sin(2*PI*440*t)):d=12:s=44100",
		"-g", "24", "-shortest", clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req.Input = clip
	result, err := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}.Run(ctx, req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(result.Frames); n < 6 || n > 10 {
		t.Fatalf("got %d keyframes, want about one per second of the 8 s window", n)
	}
	for _, frame := range result.Frames {
		if frame.Seconds < 1.9 || frame.Seconds > 10.1 {
			t.Fatalf("frame at %.3f s is outside the window in media time", frame.Seconds)
		}
		if len(frame.PBlack) != 3 || frame.PBlack[2] < 85 || frame.YMax-frame.YLow < 60 || frame.SatLow >= 10 {
			t.Fatalf("frame %+v does not look like a white bar on black", frame)
		}
	}
	if len(result.Silences) != 1 || math.Abs(result.Silences[0].Start-4) > 0.05 {
		t.Fatalf("silences %+v, want one starting at 4 s", result.Silences)
	}
}

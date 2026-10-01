package mediasample

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
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

// TestRunSamplesWithRealFFmpeg samples a generated clip with a keyframe every
// second, black until 16 s and white after, every three seconds from 0.5 s,
// in containers the concat demuxer reads (Matroska, MP4, their timestamps
// shifted to start at 11.4 s) and one it cannot (MPEG-TS). Each frame must
// carry its sample time and show the picture at that time in media time; the
// sample at 15.5 s must decode the keyframe at 15 s (black), not the next one.
func TestRunSamplesWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	var seconds []float64
	for at := 0.5; at < 30; at += 3 {
		seconds = append(seconds, at)
	}
	req := Request{
		Samples: &Samples{Seconds: seconds},
		Stats:   &StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 160, BlackThresholds: []int{32}},
		Threads: 1,
	}
	if err := caps.Require(req); err != nil {
		t.Skipf("ffmpeg cannot sample frame statistics: %v", err)
	}
	dir := t.TempDir()
	// A path with a quote and a backslash exercises the list's escaping.
	clip := filepath.Join(dir, `it's a \ clip.mkv`)
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=320x240:r=24:d=16",
		"-f", "lavfi", "-i", "color=c=white:s=320x240:r=24:d=14",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]",
		"-g", "24", "-keyint_min", "24", clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	remux := func(name string, options ...string) string {
		path := filepath.Join(dir, name)
		args := append([]string{"-hide_banner", "-loglevel", "error", "-i", clip, "-c", "copy"}, options...)
		if output, err := exec.Command(ffmpeg, append(args, path)...).CombinedOutput(); err != nil {
			t.Skipf("cannot remux the clip to %s: %v: %s", name, err, output)
		}
		return path
	}
	inputs := map[string]string{
		"matroska":         clip,
		"matroska shifted": remux("shifted.mkv", "-output_ts_offset", "11.4"),
		"mp4 shifted":      remux("shifted.mp4", "-output_ts_offset", "11.4"),
		"mpegts":           remux("clip.ts"),
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			req := req
			req.Input = input
			result, err := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}.Run(ctx, req)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(result.Frames) != len(seconds) {
				t.Fatalf("got %d frames, want one per sample (%d): %+v", len(result.Frames), len(seconds), result.Frames)
			}
			for i, frame := range result.Frames {
				if frame.Seconds != seconds[i] {
					t.Fatalf("frame %d at %.3f s, want its sample time %.3f s", i, frame.Seconds, seconds[i])
				}
				black := frame.PBlack[0] >= 90
				if wantBlack := seconds[i] < 16; black != wantBlack {
					t.Fatalf("frame for %.1f s black=%t (pblack %d), want %t", seconds[i], black, frame.PBlack[0], wantBlack)
				}
			}
		})
	}
}

// TestRunImageWithRealFFmpeg extracts single frames of a generated clip,
// black until 2 s and white after: the frame at 3 s must be white, at its
// source size and scaled, and a time past the end must fail.
func TestRunImageWithRealFFmpeg(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=320x240:r=24:d=2",
		"-f", "lavfi", "-i", "color=c=white:s=320x240:r=24:d=2",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-g", "24", clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	runner := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Thumbnail}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, width := range []int{0, 160} {
		req := Request{Input: clip, At: &At{Seconds: 3}, Images: &ImageOutput{Width: width}}
		result, err := runner.Run(ctx, req)
		if err != nil {
			t.Fatalf("width %d: %v", width, err)
		}
		if len(result.Images) != 1 || result.Images[0].Seconds != 3 {
			t.Fatalf("width %d: images %+v, want one at 3 s", width, result.Images)
		}
		picture, err := jpeg.Decode(bytes.NewReader(result.Images[0].JPEG))
		if err != nil {
			t.Fatalf("width %d: decode image: %v", width, err)
		}
		bounds := picture.Bounds()
		wantWidth := 320
		if width > 0 {
			wantWidth = width
		}
		if bounds.Dx() != wantWidth {
			t.Fatalf("image is %d px wide, want %d", bounds.Dx(), wantWidth)
		}
		if luma, _, _, _ := picture.At(bounds.Dx()/2, bounds.Dy()/2).RGBA(); luma < 0xe000 {
			t.Fatalf("width %d: frame at 3 s is not white (red %#x)", width, luma)
		}
	}
	// Depending on the version, ffmpeg writes nothing and succeeds (empty) or
	// fails because nothing was written (exit).
	_, err := runner.Run(ctx, Request{Input: clip, At: &At{Seconds: 60}, Images: &ImageOutput{}})
	var runErr *Error
	if !errors.As(err, &runErr) || (runErr.Reason != ReasonEmpty && runErr.Reason != ReasonExit) {
		t.Fatalf("frame past the end: error %v, want no image", err)
	}
}

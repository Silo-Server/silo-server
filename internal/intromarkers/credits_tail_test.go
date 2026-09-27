package intromarkers

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func TestCreditsTailRequest(t *testing.T) {
	candidate := Candidate{FileID: 7, FilePath: "/media/show/e1.mkv", DurationSeconds: 1500, CodecVideo: "h264", CodecAudio: "aac"}
	window := tailWindow(candidate)
	req := creditsTailRequest(WithPlaybackPriority(context.Background()), candidate, window, true)
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if req.Input != candidate.FilePath || *req.Window != (mediasample.Window{StartSeconds: 1050, DurationSeconds: 450, KeyframesOnly: true}) ||
		req.Threads != 1 || req.Background {
		t.Fatalf("request %+v", req)
	}
	wantStats := mediasample.StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 480, BlackThresholds: []int{20, 26, 32}}
	if req.Stats == nil || req.Stats.CropWidth != wantStats.CropWidth || req.Stats.CropHeight != wantStats.CropHeight ||
		req.Stats.Width != wantStats.Width || len(req.Stats.BlackThresholds) != 3 || req.Stats.BlackThresholds[0] != 20 || req.Stats.BlackThresholds[2] != 32 {
		t.Fatalf("stats %+v", req.Stats)
	}
	if req.Audio == nil || !req.Audio.Fingerprint || *req.Audio.Silence != (mediasample.SilenceParams{NoiseDB: -50, MinSeconds: 0.5}) {
		t.Fatalf("audio %+v", req.Audio)
	}

	if req := creditsTailRequest(context.Background(), candidate, window, false); req.Audio.Fingerprint || !req.Background {
		t.Fatalf("request without a fingerprint %+v", req)
	}
	candidate.CodecAudio = ""
	if req := creditsTailRequest(context.Background(), candidate, window, true); req.Audio != nil || req.Stats == nil {
		t.Fatalf("request for a file without audio %+v", req)
	}
	candidate.CodecAudio, candidate.CodecVideo = "aac", ""
	if req := creditsTailRequest(context.Background(), candidate, window, true); req.Stats != nil || req.Window.KeyframesOnly || req.Audio == nil {
		t.Fatalf("request for a file without video %+v", req)
	}
}

func TestCreditsTailKeyIsNamespaced(t *testing.T) {
	tail, fingerprint := creditsTailKey(), creditsFingerprintKey()
	if tail.Kind != ArtifactKindCreditsTail || tail.ConfigHash == fingerprint.ConfigHash {
		t.Fatalf("tail key %+v must not share the fingerprint key %+v", tail, fingerprint)
	}
	if tail.ConfigHash != ArtifactConfigHash(ArtifactKindCreditsTail, creditsTailParams) {
		t.Fatalf("tail key %+v is not derived from its kind and parameters", tail)
	}
	want := "tail=450:0.40;crop=0.90x0.80;width=480;black=20,26,32;silence=-50:0.50;keyframes;format=credits-tail:v1"
	if creditsTailParams != want {
		t.Fatalf("tail parameters %q, want %q", creditsTailParams, want)
	}
}

func TestTailUnusable(t *testing.T) {
	for codec, want := range map[string]string{"": tailDetailNoVideo, "prores": tailDetailUnsupportedCodec, " MJPEG ": tailDetailUnsupportedCodec, "hevc": ""} {
		if got := tailUnusableBeforeSampling(Candidate{CodecVideo: codec}); got != want {
			t.Errorf("codec %q: %q, want %q", codec, got, want)
		}
	}
	window := fingerprintWindow{Start: 1050, End: 1500}
	for frames, want := range map[int]string{0: tailDetailSparse, 14: tailDetailSparse, 15: "", 5000: "", 5001: tailDetailTooManyKeyframes} {
		if got := tailUnusableAfterSampling(frames, window); got != want {
			t.Errorf("%d keyframes: %q, want %q", frames, got, want)
		}
	}
}

// TestSampleCreditsTailWithRealFFmpeg runs the tail pass on a generated
// episode: a dark, noisy scene with a bright highlight, then 30 seconds of
// text bars on black, one keyframe a second.
func TestSampleCreditsTailWithRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	caps, err := mediasample.LoadCapabilities(ctx, ffmpeg)
	if err != nil {
		t.Skipf("ffmpeg capabilities unavailable: %v", err)
	}
	candidate := Candidate{FileID: 1, DurationSeconds: 100, CodecVideo: "h264", CodecAudio: "aac"}
	if err := caps.Require(creditsTailRequest(ctx, candidate, tailWindow(candidate), false)); err != nil {
		t.Skipf("ffmpeg cannot run the tail pass: %v", err)
	}
	for _, filter := range []string{"drawbox", "noise", "concat", "aevalsrc"} {
		if !caps.HasFilter(filter) {
			t.Skipf("ffmpeg lacks the %s filter the clip needs", filter)
		}
	}
	candidate.FilePath = filepath.Join(t.TempDir(), "episode.mkv")
	scene := "color=c=0x262626:s=640x360:r=10:d=70,noise=alls=24:allf=t,drawbox=x=500:y=40:w=24:h=24:color=white:t=fill"
	credits := "color=c=black:s=640x360:r=10:d=30,drawbox=x=200:y=150:w=240:h=10:color=white:t=fill," +
		"drawbox=x=240:y=190:w=160:h=10:color=white:t=fill"
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", scene, "-f", "lavfi", "-i", credits,
		"-f", "lavfi", "-i", "aevalsrc=0.3*sin(2*PI*330*t):d=100:s=44100",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-map", "2:a",
		"-g", "10", "-shortest", candidate.FilePath)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}

	extractor := NewChromaprintExtractor(DefaultConfig(ffmpeg))
	sample, err := extractor.SampleCreditsTail(ctx, candidate, false)
	if err != nil {
		t.Fatalf("SampleCreditsTail: %v", err)
	}
	if sample.Fingerprint != nil {
		t.Fatalf("fingerprint %+v, want none when not asked", sample.Fingerprint)
	}
	keyframes := classifyKeyframes(sample.Tail.Frames)
	if len(keyframes) < 35 || len(keyframes) > 45 {
		t.Fatalf("%d keyframes in the 40 s tail, want about one a second", len(keyframes))
	}
	for _, keyframe := range keyframes {
		want := keyframeContent
		if keyframe.Seconds >= 70.5 {
			want = keyframeLettered
		}
		if keyframe.Seconds > 69.5 && keyframe.Seconds < 70.5 {
			continue
		}
		if keyframe.Class != want {
			t.Fatalf("keyframe at %.2f s is %v, want %v (all: %+v)", keyframe.Seconds, keyframe.Class, want, keyframes)
		}
	}
	segment, ok := combineCredits(candidate, creditsEvidence{Keyframes: keyframes, Silences: sample.Tail.Silences}, creditsLimitsFor(false))
	if !ok || math.Abs(segment.Start-70) > 1 || segment.End != 100 || segment.Algorithm != CreditsVideoAlgorithm {
		t.Fatalf("credits %+v (placed %t), want video credits from 70 s to the end", segment, ok)
	}
}

// TestSampleCreditsTailWithoutTheProbedAudio runs the tail pass on a file
// with no audio stream whose probe metadata names one. ffmpeg fails a run
// whose audio output finds no stream, so the video is sampled alone.
func TestSampleCreditsTailWithoutTheProbedAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	caps, err := mediasample.LoadCapabilities(ctx, ffmpeg)
	if err != nil {
		t.Skipf("ffmpeg capabilities unavailable: %v", err)
	}
	candidate := Candidate{FileID: 1, DurationSeconds: 100, CodecVideo: "h264", CodecAudio: "aac"}
	if err := caps.Require(creditsTailRequest(ctx, candidate, tailWindow(candidate), false)); err != nil {
		t.Skipf("ffmpeg cannot run the tail pass: %v", err)
	}
	candidate.FilePath = filepath.Join(t.TempDir(), "episode.mkv")
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=320x180:r=10:d=100", "-g", "10", candidate.FilePath)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}

	extractor := NewChromaprintExtractor(DefaultConfig(ffmpeg))
	for _, fingerprint := range []bool{false, true} {
		if fingerprint && caps.Require(creditsTailRequest(ctx, candidate, tailWindow(candidate), true)) != nil {
			continue
		}
		sample, err := extractor.SampleCreditsTail(ctx, candidate, fingerprint)
		if err != nil {
			t.Fatalf("SampleCreditsTail(fingerprint=%t): %v", fingerprint, err)
		}
		if n := len(sample.Tail.Frames); n < 35 || n > 45 {
			t.Fatalf("fingerprint=%t: %d keyframes in the 40 s tail, want about one a second", fingerprint, n)
		}
		if len(sample.Tail.Silences) != 0 {
			t.Fatalf("fingerprint=%t: silences %+v from a file without audio", fingerprint, sample.Tail.Silences)
		}
		// An asked-for fingerprint comes back empty, which stores the file
		// as having no audio to fingerprint.
		if fingerprint != (sample.Fingerprint != nil) || (sample.Fingerprint != nil && len(sample.Fingerprint.Points) != 0) {
			t.Fatalf("fingerprint=%t: fingerprint %+v", fingerprint, sample.Fingerprint)
		}
	}
}

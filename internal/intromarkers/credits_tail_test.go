package intromarkers

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func TestCreditsTailKeyIsNamespaced(t *testing.T) {
	tail, fingerprint := creditsTailKey(), creditsFingerprintKey()
	if tail.Kind != ArtifactKindCreditsTail || tail.ConfigHash == fingerprint.ConfigHash {
		t.Fatalf("tail key %+v must not share the fingerprint key %+v", tail, fingerprint)
	}
	if tail.ConfigHash != mediaartifact.ConfigHash(ArtifactKindCreditsTail, creditsTailParams) {
		t.Fatalf("tail key %+v is not derived from its kind and parameters", tail)
	}
	want := "tail=450:0.40;crop=0.90x0.80;width=480;black=20,26,32;silence=-50:0.50;keyframes;format=credits-tail:v1"
	if creditsTailParams != want {
		t.Fatalf("tail parameters %q, want %q", creditsTailParams, want)
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
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-shortest", candidate.FilePath)
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

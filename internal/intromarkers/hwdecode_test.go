package intromarkers

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// TestSampleCreditsTailFallsBackFromBrokenHardware runs the tail pass on a
// VAAPI device that does not exist: the hardware attempt fails in ffmpeg,
// and the software attempt produces the tail.
func TestSampleCreditsTailFallsBackFromBrokenHardware(t *testing.T) {
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
	candidate := Candidate{FileID: 1, DurationSeconds: 120, CodecVideo: "h264"}
	if err := caps.Require(creditsTailRequest(ctx, candidate, tailWindow(candidate), false)); err != nil {
		t.Skipf("ffmpeg cannot run the tail pass: %v", err)
	}
	candidate.FilePath = filepath.Join(t.TempDir(), "episode.mkv")
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=320x180:r=10:d=120",
		"-c:v", "libx264", "-threads", "1", "-g", "5", "-keyint_min", "5", "-pix_fmt", "yuv420p", "-y", candidate.FilePath)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}

	cfg := DefaultConfig(ffmpeg)
	cfg.HWAccel, cfg.HWDevice = "vaapi", filepath.Join(t.TempDir(), "renderD128")
	extractor := NewChromaprintExtractor(cfg)
	sample, err := extractor.SampleCreditsTail(ctx, candidate, false)
	if err != nil {
		t.Fatalf("SampleCreditsTail: %v", err)
	}
	if len(sample.Tail.Frames) < 90 {
		t.Fatalf("%d keyframes in the 48 s tail, want two a second from the software attempt", len(sample.Tail.Frames))
	}
}

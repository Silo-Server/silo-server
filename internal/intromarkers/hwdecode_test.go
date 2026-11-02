package intromarkers

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// fakeResolve resolves every configured accelerator to resolved and counts
// its calls.
func fakeResolve(resolved string, calls *int) func(context.Context, string, string, string) string {
	return func(_ context.Context, hwAccel, _, _ string) string {
		*calls++
		if hwAccel == "auto" {
			return resolved
		}
		return hwAccel
	}
}

func TestTailRunnerDecodesVideoOnResolvedHardware(t *testing.T) {
	cfg := DefaultConfig("/test/ffmpeg")
	cfg.HWAccel, cfg.HWDevice = "auto", "/dev/dri/renderD128,/dev/dri/renderD129"
	extractor := NewChromaprintExtractor(cfg)
	var calls int
	extractor.hardware.Resolve = fakeResolve("vaapi", &calls)
	candidate := Candidate{FileID: 7, FilePath: "/media/show/e1.mkv", DurationSeconds: 1500, CodecVideo: "h264", CodecAudio: "aac"}
	hardwareFirst := []mediasample.Attempt{{Hardware: true}, {}}

	req := creditsTailRequest(context.Background(), candidate, tailWindow(candidate), true)
	runner := extractor.tailRunner(context.Background(), &req)
	if !reflect.DeepEqual(req.Attempts, hardwareFirst) || runner.HWAccel != "vaapi" || runner.HWDevice != cfg.HWDevice {
		t.Fatalf("attempts %+v on %q/%q, want hardware then software on vaapi with the configured devices", req.Attempts, runner.HWAccel, runner.HWDevice)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	movie := movieTailRequest(context.Background(), candidate, fingerprintWindow{Start: 900, End: 1500})
	if extractor.tailRunner(context.Background(), &movie); !reflect.DeepEqual(movie.Attempts, hardwareFirst) || movie.Validate() != nil {
		t.Fatalf("movie attempts %+v, want hardware then software", movie.Attempts)
	}
	if calls != 1 {
		t.Fatalf("resolved %d times, want once per configured pair", calls)
	}

	// Audio decodes in software only.
	candidate.CodecVideo = ""
	audio := creditsTailRequest(context.Background(), candidate, tailWindow(candidate), true)
	if runner := extractor.tailRunner(context.Background(), &audio); audio.Attempts != nil || runner.HWAccel != "" {
		t.Fatalf("audio-only attempts %+v on %q, want one software attempt", audio.Attempts, runner.HWAccel)
	}

	for accel, want := range map[string][]mediasample.Attempt{"none": nil, "nvenc": nil, "videotoolbox": hardwareFirst, "qsv": hardwareFirst} {
		extractor.hardware.Set(accel, "")
		req := movieTailRequest(context.Background(), candidate, fingerprintWindow{Start: 900, End: 1500})
		runner := extractor.tailRunner(context.Background(), &req)
		if !reflect.DeepEqual(req.Attempts, want) || (want != nil) != (runner.HWAccel == accel) {
			t.Errorf("%s: attempts %+v on %q, want %+v", accel, req.Attempts, runner.HWAccel, want)
		}
	}
}

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
	var log bytes.Buffer
	extractor.logger = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sample, err := extractor.SampleCreditsTail(ctx, candidate, false)
	if err != nil {
		t.Fatalf("SampleCreditsTail: %v", err)
	}
	if len(sample.Tail.Frames) < 90 {
		t.Fatalf("%d keyframes in the 48 s tail, want two a second from the software attempt", len(sample.Tail.Frames))
	}
	if !strings.Contains(log.String(), "decoder=software hardware_fallback=true") {
		t.Fatalf("log %q, want the software decoder after a hardware failure", log.String())
	}
	if !strings.Contains(log.String(), `level=WARN msg="credits tail hardware decode failed; using software" decoder=hardware:vaapi`) {
		t.Fatalf("log %q, want the hardware failure at warn level", log.String())
	}
}

func TestTailRequestsCarryTheVideoBitDepth(t *testing.T) {
	candidate := Candidate{FileID: 1, FilePath: "/media/movie.mkv", DurationSeconds: 7200, CodecVideo: "hevc", CodecAudio: "aac", VideoBitDepth: 10}
	window := fingerprintWindow{Start: 6600, End: 7200}
	if depth := creditsTailRequest(context.Background(), candidate, window, false).VideoBitDepth; depth != 10 {
		t.Errorf("episode tail request bit depth %d, want 10", depth)
	}
	if depth := movieTailRequest(context.Background(), candidate, window).VideoBitDepth; depth != 10 {
		t.Errorf("movie tail request bit depth %d, want 10", depth)
	}

	// A probed depth past what hardware download formats cover only picks
	// the VideoToolbox format, so it becomes unknown rather than failing
	// the request that software can decode.
	candidate.VideoBitDepth = 32
	for name, req := range map[string]mediasample.Request{
		"episode": creditsTailRequest(context.Background(), candidate, window, false),
		"movie":   movieTailRequest(context.Background(), candidate, window),
	} {
		if req.VideoBitDepth != 0 {
			t.Errorf("%s tail request bit depth %d for a 32-bit probe, want 0 (unknown)", name, req.VideoBitDepth)
		}
		if err := req.Validate(); err != nil {
			t.Errorf("%s tail request for a 32-bit probe: %v", name, err)
		}
	}
}

// TestSampleTailsDecodeASourceWithAnImplausibleBitDepth runs both tail passes
// in software on a candidate whose probe reported 32 bits.
func TestSampleTailsDecodeASourceWithAnImplausibleBitDepth(t *testing.T) {
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
	candidate := Candidate{FileID: 1, DurationSeconds: 10, CodecVideo: "h264", VideoBitDepth: 32}
	if err := caps.Require(creditsTailRequest(ctx, candidate, tailWindow(candidate), false)); err != nil {
		t.Skipf("ffmpeg cannot run the tail pass: %v", err)
	}
	candidate.FilePath = filepath.Join(t.TempDir(), "source.mkv")
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=320x180:r=10:d=10",
		"-c:v", "libx264", "-threads", "1", "-g", "10", "-pix_fmt", "yuv420p", "-y", candidate.FilePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create tail fixture: %v: %s", err, output)
	}

	extractor := NewChromaprintExtractor(DefaultConfig(ffmpeg))
	sample, err := extractor.SampleCreditsTail(ctx, candidate, false)
	if err != nil {
		t.Fatalf("SampleCreditsTail: %v", err)
	}
	if len(sample.Tail.Frames) == 0 {
		t.Fatal("episode tail has no keyframes")
	}
	tail, err := extractor.SampleMovieTail(ctx, candidate)
	if err != nil {
		t.Fatalf("SampleMovieTail: %v", err)
	}
	if len(tail.Frames) == 0 {
		t.Fatal("movie tail has no keyframes")
	}
}

// TestTailRunnerTreatsAMissingStreamAsNoHardwareFailure fails a hardware
// attempt because an output found no stream, which is the input's fault: the
// run must end there without a hardware warning, so the caller's video-only
// retry runs on hardware, and a later genuine failure still warns.
func TestTailRunnerTreatsAMissingStreamAsNoHardwareFailure(t *testing.T) {
	cfg := DefaultConfig("/test/ffmpeg")
	cfg.HWAccel = "vaapi"
	extractor := NewChromaprintExtractor(cfg)
	var calls int
	extractor.hardware.Resolve = fakeResolve("vaapi", &calls)
	var log bytes.Buffer
	extractor.logger = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	candidate := Candidate{FileID: 7, FilePath: "/media/show/e1.mkv", DurationSeconds: 1500, CodecVideo: "h264", CodecAudio: "aac"}
	req := creditsTailRequest(context.Background(), candidate, tailWindow(candidate), false)
	runner := extractor.tailRunner(context.Background(), &req)
	hardware := mediasample.Attempt{Hardware: true}

	noStream := mediasample.AttemptError{Decoder: "hardware:vaapi", Reason: mediasample.ReasonExit,
		StderrTail: "[out#0/null @ 0x1] Output file does not contain any stream\nError opening output file -."}
	if runner.Fallback(hardware, noStream) {
		t.Fatal("a hardware attempt that found no stream moved on to software")
	}
	if log.Len() != 0 {
		t.Fatalf("log %q, want no hardware failure for a missing stream", log.String())
	}

	failed := mediasample.AttemptError{Decoder: "hardware:vaapi", Reason: mediasample.ReasonExit,
		StderrTail: "Device creation failed: -5.\nFailed to set value 'vaapi=hw:/dev/dri/renderD128' for option 'init_hw_device': I/O error"}
	if !runner.Fallback(hardware, failed) {
		t.Fatal("a failed hardware attempt did not move on to software")
	}
	if !strings.Contains(log.String(), `level=WARN msg="credits tail hardware decode failed; using software" decoder=hardware:vaapi`) {
		t.Fatalf("log %q, want the genuine hardware failure at warn level", log.String())
	}
}

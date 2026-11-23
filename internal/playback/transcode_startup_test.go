package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/procgroup"
	"github.com/Silo-Server/silo-server/internal/procgroup/procgrouptest"
)

const startupTestManifest = "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nseg_00000.ts\n#EXT-X-ENDLIST\n"

// fakeStartupSession returns a session whose process has already exited
// (ready writes a manifest first) or, when running, is still starting.
func fakeStartupSession(t *testing.T, opts TranscodeOpts, outputDir string, ready, running bool) *TranscodeSession {
	t.Helper()
	session := &TranscodeSession{
		opts:                opts,
		outputDir:           outputDir,
		running:             running,
		stderr:              newBoundedTailBuffer(stderrTailMaxBytes),
		generationStartedAt: time.Now().Add(-time.Second),
		inheritedManifest:   statManifest(outputDir),
	}
	if ready {
		if err := os.WriteFile(filepath.Join(outputDir, "stream.m3u8"), []byte(startupTestManifest), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if !running {
		session.waitErr = errors.New("gpu failed")
	}
	return session
}

func startupTestOpts() TranscodeOpts {
	opts := resolvedAutoOpts()
	opts.SessionID = "playback-1"
	opts.HWDevice = "gpu-0,gpu-1"
	opts.TargetCodecAudio = "aac"
	opts.SegmentDuration = 2
	return opts
}

func TestStartReadyTranscodeSpawnErrorIsTerminal(t *testing.T) {
	spawnErr := errors.New("mkdir failed")
	for name, failOn := range map[string]int{"first attempt": 1, "after an exit": 2} {
		t.Run(name, func(t *testing.T) {
			cache := newAutoTranscodePipelineCache()
			pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), cache)
			attempts := 0
			start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
				attempts++
				if attempts == failOn {
					return nil, spawnErr
				}
				return fakeStartupSession(t, opts, t.TempDir(), false, false), nil
			}

			_, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
			if !errors.Is(err, spawnErr) {
				t.Fatalf("error = %v, want spawn error", err)
			}
			var startupErr *TranscodeStartupError
			if errors.As(err, &startupErr) {
				t.Fatal("spawn error must not be reported as a readiness failure")
			}
			if attempts != failOn {
				t.Fatalf("attempt count = %d, want %d", attempts, failOn)
			}
			if len(cache.preferred) != 0 {
				t.Fatal("a failure must never be cached")
			}
		})
	}
}

func TestStartReadyTranscodeEnabledPipelineSkipsLegacyRetry(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), newAutoTranscodePipelineCache())
	attempts := 0
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts++
		return fakeStartupSession(t, opts, t.TempDir(), false, false), nil
	}

	_, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{
		Timeout:     time.Millisecond,
		Start:       start,
		LegacyRetry: TranscodeStartupRetryOtherDevice,
	})
	var startupErr *TranscodeStartupError
	if !errors.As(err, &startupErr) || startupErr.WasRunning {
		t.Fatalf("error = %v, want readiness failure after exit", err)
	}
	if attempts != 3 {
		t.Fatalf("attempt count = %d, want the three pipeline stages", attempts)
	}
}

func TestStartReadyTranscodeDisabledPipelineLegacyRetry(t *testing.T) {
	base := startupTestOpts()
	base.HWAccel = HWAccelNone
	tests := []struct {
		name         string
		policy       TranscodeStartupRetry
		wantAttempts int
	}{
		{name: "none", policy: TranscodeStartupRetryNone, wantAttempts: 1},
		{name: "other device", policy: TranscodeStartupRetryOtherDevice, wantAttempts: 2},
		{name: "accel change without a change", policy: TranscodeStartupRetryAccelChange, wantAttempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := newAutoTranscodePipeline(context.Background(), base, newAutoTranscodePipelineCache())
			if pipeline.Enabled() {
				t.Fatal("explicit none must not enable the pipeline")
			}
			var attempts []TranscodeOpts
			start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
				attempts = append(attempts, opts)
				opts.HWDevice = "gpu-0"
				return fakeStartupSession(t, opts, t.TempDir(), false, false), nil
			}

			_, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start, LegacyRetry: tt.policy})
			if err == nil {
				t.Fatal("expected startup failure")
			}
			if len(attempts) != tt.wantAttempts {
				t.Fatalf("attempt count = %d, want %d", len(attempts), tt.wantAttempts)
			}
			if tt.wantAttempts == 2 && attempts[1].AvoidHWDevice != "gpu-0" {
				t.Fatalf("legacy retry AvoidHWDevice = %q, want gpu-0", attempts[1].AvoidHWDevice)
			}
		})
	}
}

// A recipe card can be rebuilt where the GPU decoder rejects its source: on
// another node, or after the operator chose this backend. An explicit backend
// has no fallback order, so the reconstruct must still move to CPU decoding,
// in the same directory, after the request that triggered it has gone.
func TestStartReconstructTranscodeRetriesDecoderRejectionOnCPU(t *testing.T) {
	opts := startupTestOpts()
	opts.HWAccel = transcodeHWNVENC
	opts.HWDevice = ""
	pipeline := newAutoTranscodePipeline(context.Background(), opts, newAutoTranscodePipelineCache())
	outputDir := t.TempDir()
	servedSegment := filepath.Join(outputDir, "seg_00010.ts")
	if err := os.WriteFile(servedSegment, []byte("segment"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts []TranscodeOpts
	start := func(startCtx context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts = append(attempts, opts)
		if len(attempts) > 1 && startCtx.Err() != nil {
			t.Errorf("attempt %d launched on the ended request's context", len(attempts))
		}
		if _, err := os.Stat(servedSegment); err != nil {
			t.Fatalf("attempt %d: rejected attempt removed the shared output dir: %v", len(attempts), err)
		}
		session := fakeStartupSession(t, opts, outputDir, len(attempts) == 2, false)
		if len(attempts) == 1 {
			_, _ = session.stderr.Write([]byte("[hevc @ 0x1] Hardware is lacking required capabilities"))
			cancel()
		}
		return session, nil
	}

	session, err := StartReconstructTranscode(ctx, pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
	if err != nil {
		t.Fatalf("StartReconstructTranscode: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want GPU decode then CPU decode", len(attempts))
	}
	if got := session.Opts(); got.HWAccel != transcodeHWNVENC || !got.SoftwareVideoDecode {
		t.Fatalf("returned attempt = %s software decode %v, want NVENC with CPU decode", got.HWAccel, got.SoftwareVideoDecode)
	}
}

// seedPreviousGeneration leaves the manifest and segment an earlier
// generation wrote into outputDir, as a restart-surviving directory holds.
func seedPreviousGeneration(t *testing.T, outputDir string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	for name, data := range map[string]string{"stream.m3u8": startupTestManifest, "seg_00000.ts": "segment"} {
		path := filepath.Join(outputDir, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStartReconstructTranscodeIgnoresPreviousGenerationManifest(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), newAutoTranscodePipelineCache())
	outputDir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedPreviousGeneration(t, outputDir)
	var attempts []TranscodeOpts
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		if _, err := os.Stat(outputDir); err != nil {
			t.Fatalf("output dir removed before final failure: %v", err)
		}
		attempts = append(attempts, opts)
		return fakeStartupSession(t, opts, outputDir, false, false), nil
	}

	session, err := StartReconstructTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
	var startupErr *TranscodeStartupError
	if session != nil || !errors.As(err, &startupErr) {
		t.Fatalf("session returned = %t, error = %v; want readiness failure", session != nil, err)
	}
	if len(attempts) != 3 {
		t.Fatalf("attempt count = %d, want full hardware, mixed, software", len(attempts))
	}
	if got := attempts[2]; got.HWAccel != HWAccelNone || !got.SoftwareVideoDecode {
		t.Fatalf("final attempt = %+v, want software path", got)
	}
	if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
		t.Fatalf("final reconstruct failure kept output: %v", err)
	}
}

// A filesystem whose clock runs behind this host stamps fresh output with an
// earlier time. Readiness must follow file identity, not timestamps.
func TestWaitForGenerationManifestAcceptsFreshManifestWithSkewedClock(t *testing.T) {
	outputDir := t.TempDir()
	seedPreviousGeneration(t, outputDir)
	opts := startupTestOpts()
	opts.HWAccel = HWAccelNone
	session := fakeStartupSession(t, opts, outputDir, false, true)

	// FFmpeg's temp_file flag replaces the playlist through a rename.
	tmp := filepath.Join(outputDir, "stream.m3u8.tmp")
	if err := os.WriteFile(tmp, []byte(startupTestManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	skewed := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(tmp, skewed, skewed); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(outputDir, "stream.m3u8")); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	session.running = false
	session.mu.Unlock()

	if _, err := session.WaitForGenerationManifest(time.Millisecond); err != nil {
		t.Fatalf("WaitForGenerationManifest with a skewed manifest mtime: %v", err)
	}
}

func TestStartReadyTranscodeStopsWaitingWhenRequestEnds(t *testing.T) {
	for _, running := range []bool{true, false} {
		cache := newAutoTranscodePipelineCache()
		pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), cache)
		first := pipeline.Current()
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		var started *TranscodeSession
		start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
			attempts++
			started = fakeStartupSession(t, opts, t.TempDir(), false, running)
			cancel()
			return started, nil
		}

		begin := time.Now()
		_, err := StartReadyTranscode(ctx, pipeline, TranscodeStartup{Timeout: time.Minute, Start: start})
		if elapsed := time.Since(begin); elapsed > 10*time.Second {
			t.Fatalf("running=%v: startup waited %v after the request ended", running, elapsed)
		}
		var startupErr *TranscodeStartupError
		if !errors.As(err, &startupErr) {
			t.Fatalf("running=%v: error = %v, want TranscodeStartupError", running, err)
		}
		if running && !errors.Is(err, context.Canceled) {
			t.Fatalf("running=%v: error = %v, want context.Canceled cause", running, err)
		}
		if attempts != 1 {
			t.Fatalf("running=%v: attempt count = %d, want 1 (no fallback for a departed request)", running, attempts)
		}
		if started.IsRunning() {
			t.Fatalf("running=%v: abandoned attempt was not closed", running)
		}
		if got := pipeline.Current(); got.HWAccel != first.HWAccel || got.SoftwareVideoDecode != first.SoftwareVideoDecode || got.AvoidHWDevice != first.AvoidHWDevice {
			t.Fatalf("running=%v: pipeline advanced to %+v after a cancellation", running, got)
		}
		if len(cache.preferred) != 0 {
			t.Fatalf("running=%v: a cancellation must never be cached", running)
		}
	}
}

// An explicit NVENC start on a card whose NVDEC cannot decode the source
// (HEVC on a Maxwell GTX 970) failed the same way on its retry, because the
// retry changed only the render device. A decoder rejection now walks CPU
// decode with NVENC, then software; any other early exit keeps the old retry.
func TestStartReadyTranscodeRetriesDecoderRejectionOnCPU(t *testing.T) {
	const nvdecRejection = "[hevc @ 0x1] Hardware is lacking required capabilities\n" +
		"[hevc @ 0x1] Failed setup for format cuda: hwaccel initialisation returned error."
	base := startupTestOpts()
	base.HWAccel = transcodeHWNVENC
	base.HWDevice = ""
	tests := []struct {
		name      string
		policy    TranscodeStartupRetry
		stderr    string
		readyOn   int
		wantAccel []string
	}{
		{name: "mixed stage plays", policy: TranscodeStartupRetryAccelChange, stderr: nvdecRejection, readyOn: 2,
			wantAccel: []string{transcodeHWNVENC, transcodeHWNVENC}},
		{name: "software after mixed fails", policy: TranscodeStartupRetryOtherDevice, stderr: nvdecRejection,
			wantAccel: []string{transcodeHWNVENC, transcodeHWNVENC, HWAccelNone}},
		{name: "rejection after long output", policy: TranscodeStartupRetryAccelChange, stderr: strings.Repeat("x", 600) + "\n" + nvdecRejection, readyOn: 2,
			wantAccel: []string{transcodeHWNVENC, transcodeHWNVENC}},
		{name: "no retry policy", policy: TranscodeStartupRetryNone, stderr: nvdecRejection,
			wantAccel: []string{transcodeHWNVENC}},
		{name: "other failure keeps accel change rule", policy: TranscodeStartupRetryAccelChange, stderr: "[vost#0:0/h264_nvenc] Could not open encoder",
			wantAccel: []string{transcodeHWNVENC}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := newAutoTranscodePipeline(context.Background(), base, newAutoTranscodePipelineCache())
			if pipeline.Enabled() {
				t.Fatal("explicit nvenc must not enable the auto pipeline")
			}
			var attempts []TranscodeOpts
			start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
				attempts = append(attempts, opts)
				session := fakeStartupSession(t, opts, t.TempDir(), len(attempts) == tt.readyOn, false)
				_, _ = session.stderr.Write([]byte(tt.stderr))
				return session, nil
			}

			_, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start, LegacyRetry: tt.policy})
			if (err == nil) != (tt.readyOn > 0) {
				t.Fatalf("error = %v, want ready on attempt %d", err, tt.readyOn)
			}
			if len(attempts) != len(tt.wantAccel) {
				t.Fatalf("attempt count = %d, want %d", len(attempts), len(tt.wantAccel))
			}
			for i, attempt := range attempts {
				if attempt.HWAccel != tt.wantAccel[i] {
					t.Fatalf("attempt %d HWAccel = %q, want %q", i, attempt.HWAccel, tt.wantAccel[i])
				}
				if wantCPUDecode := i > 0; attempt.SoftwareVideoDecode != wantCPUDecode {
					t.Fatalf("attempt %d SoftwareVideoDecode = %v, want %v", i, attempt.SoftwareVideoDecode, wantCPUDecode)
				}
			}
			if len(attempts) > 1 && !attempts[1].nvencSoftwareDecode {
				t.Fatal("CPU-decode retry must keep the NVENC encoder")
			}
		})
	}
}

func TestWaitForGenerationManifestIgnoresPreviousGenerationOutput(t *testing.T) {
	outputDir := t.TempDir()
	seedPreviousGeneration(t, outputDir)
	opts := startupTestOpts()
	opts.HWAccel = HWAccelNone

	running := fakeStartupSession(t, opts, outputDir, false, true)
	if _, err := running.WaitForGenerationManifest(time.Millisecond); !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("running WaitForGenerationManifest error = %v, want ErrManifestNotReady", err)
	}

	exited := fakeStartupSession(t, opts, outputDir, false, false)
	if _, err := exited.GetManifest(); err != nil {
		t.Fatalf("GetManifest: %v; want the directory's existing manifest", err)
	}
	if _, err := exited.WaitForGenerationManifest(time.Millisecond); !errors.Is(err, ErrTranscodeFailed) {
		t.Fatalf("exited WaitForGenerationManifest error = %v, want ErrTranscodeFailed", err)
	}

	if err := os.WriteFile(filepath.Join(outputDir, "stream.m3u8"), []byte(startupTestManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exited.WaitForGenerationManifest(time.Millisecond); err != nil {
		t.Fatalf("WaitForGenerationManifest after this generation wrote its manifest: %v", err)
	}
}

func TestStartReadyTranscodeDoesNotDuplicateSlowProcess(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), cache)
	attempts := 0
	var slow *TranscodeSession
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts++
		slow = fakeStartupSession(t, opts, t.TempDir(), false, true)
		return slow, nil
	}

	_, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
	var startupErr *TranscodeStartupError
	if !errors.As(err, &startupErr) || !startupErr.WasRunning {
		t.Fatalf("error = %v, want readiness failure of a running process", err)
	}
	if !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("error = %v, want ErrManifestNotReady cause", err)
	}
	if attempts != 1 {
		t.Fatalf("attempt count = %d, want 1", attempts)
	}
	if slow.IsRunning() {
		t.Fatal("slow fresh start was not closed at the deadline")
	}
	if len(cache.preferred) != 0 {
		t.Fatal("a timeout must never be cached")
	}
}

func TestStartReconstructTranscodeKeepsOutputForNextAttempt(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), newAutoTranscodePipelineCache())
	outputDir := t.TempDir()
	servedSegment := filepath.Join(outputDir, "seg_00010.ts")
	if err := os.WriteFile(servedSegment, []byte("segment"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The request that triggered the rebuild ends during the first attempt.
	// The rebuild serves every request waiting on it, so later attempts still
	// start. Like StartTranscode for some recipes, this seam returns a
	// canceled context's error.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	start := func(startCtx context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		if err := startCtx.Err(); err != nil {
			return nil, err
		}
		attempts++
		cancel()
		if _, err := os.Stat(servedSegment); err != nil {
			t.Fatalf("attempt %d: failed attempt removed the shared output dir: %v", attempts, err)
		}
		return fakeStartupSession(t, opts, outputDir, attempts == 3, false), nil
	}

	session, err := StartReconstructTranscode(ctx, pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
	if err != nil {
		t.Fatalf("StartReconstructTranscode: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempt count = %d, want 3", attempts)
	}
	if got := session.Opts(); got.HWAccel != HWAccelNone || !got.SoftwareVideoDecode {
		t.Fatalf("final attempt = %+v, want software path", got)
	}
}

// gpuDecodeRejectingFFmpeg writes a fake FFmpeg that records each
// invocation's arguments under logDir. Asked to decode on the GPU, it prints
// stderr and then exits with 218, or, when keepRunning is set, keeps running
// in a child process the way FFmpeg reads on through an input its decoder
// rejects packet by packet. Without -hwaccel it writes a playable manifest and its segments,
// and runs until killed.
func gpuDecodeRejectingFFmpeg(t *testing.T, logDir, stderr string, keepRunning bool) string {
	t.Helper()
	dir := t.TempDir()
	stderrPath := filepath.Join(dir, "stderr.txt")
	manifestPath := filepath.Join(dir, "stream.m3u8")
	if err := os.WriteFile(stderrPath, []byte(stderr), 0o600); err != nil {
		t.Fatal(err)
	}
	// A fresh start waits for minManifestSegments listed, non-empty segments.
	const manifest = "#EXTM3U\n#EXT-X-TARGETDURATION:2\n" +
		"#EXTINF:2,\nseg_00000.ts\n#EXTINF:2,\nseg_00001.ts\n#EXTINF:2,\nseg_00002.ts\n"
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	onGPUDecode := `cat "` + stderrPath + `" >&2; exit 218`
	if keepRunning {
		// The work runs in a child started first, as the real FFmpeg does
		// under a wrapper script, so stopping only the script would leave it
		// running.
		onGPUDecode = `sleep 30 & echo $! > "` + logDir + `/gpu-child.pid"; cat "` + stderrPath + `" >&2; wait; exit 218`
	}
	path := filepath.Join(dir, "ffmpeg")
	script := `#!/bin/sh
n=1
while [ -e "` + logDir + `/args-$n" ]; do n=$((n+1)); done
printf '%s\n' "$@" > "` + logDir + `/args-$n.tmp"
mv "` + logDir + `/args-$n.tmp" "` + logDir + `/args-$n"
for arg in "$@"; do
  if [ "$arg" = "-hwaccel" ]; then
    ` + onGPUDecode + `
  fi
done
for n in 0 1 2; do echo segment > "seg_0000$n.ts"; done
cp "` + manifestPath + `" stream.m3u8
while :; do sleep 0.1; done
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// An explicit QSV or VAAPI start whose GPU cannot decode the source failed on
// every executor: the start-path retry only matched NVDEC's rejection, so
// a transcode node gave up and the local retry ran the same command again. The
// stderr below is FFmpeg's, from the two reports.
//   - #4319: a codec with no GPU decoder is decoded on the CPU, and the
//     VAAPI graph cannot take the frames. FFmpeg exits at once.
//   - #4279: FFmpeg's AV1 decoder has no software path, so on a GPU without
//     AV1 decode it rejects every packet and reads on. It must be stopped
//     for the retry to run before the startup wait ends.
//
// Both now move to CPU decode with the same GPU encoder.
func TestStartReadyTranscodeDecodesOnCPUWhenQSVOrVAAPICannotDecode(t *testing.T) {
	const filterRejectsCPUFrames = "Impossible to convert between the formats supported by the filter 'graph -1 input from stream 0:0' and the filter 'auto_scale_0'\n" +
		"Link 'graph -1 input from stream 0:0.default' -> 'auto_scale_0.default':\n" +
		"  Pixel formats:\n    src: yuv420p\n    dst: yuv420p\n" +
		"[vf#0:0 @ 0x1] Error reinitializing filters!\n" +
		"[vf#0:0 @ 0x1] Task finished with error code: -38 (Function not implemented)\n" +
		"[vost#0:0/hevc_vaapi @ 0x2] [enc:hevc_vaapi @ 0x3] Could not open encoder before EOF\n"
	const av1DecoderHasNoGPU = "[av1 @ 0x1] Your platform doesn't support hardware accelerated AV1 decoding.\n" +
		"[av1 @ 0x1] Failed to get pixel format.\n" +
		"[av1 @ 0x1] Get current frame error\n" +
		"[vist#0:0/av1 @ 0x2] [dec:av1 @ 0x3] Error submitting packet to decoder: Function not implemented\n" +
		"[av1 @ 0x1] No support for codec av1 profile 0.\n" +
		"[av1 @ 0x1] Failed setup for format vaapi: hwaccel initialisation returned error.\n"
	tests := []struct {
		name        string
		hwAccel     string
		codec       string
		bitDepth    int
		policy      TranscodeStartupRetry
		stderr      string
		keepRunning bool
	}{
		{name: "VAAPI node, codec without a GPU decoder", hwAccel: transcodeHWVAAPI, codec: "theora", bitDepth: 8,
			policy: TranscodeStartupRetryAccelChange, stderr: filterRejectsCPUFrames},
		{name: "VAAPI local, AV1 without a GPU decoder", hwAccel: transcodeHWVAAPI, codec: "av1", bitDepth: 10,
			policy: TranscodeStartupRetryOtherDevice, stderr: av1DecoderHasNoGPU, keepRunning: true},
		{name: "QSV node, AV1 without a GPU decoder", hwAccel: transcodeHWQSV, codec: "av1", bitDepth: 10,
			policy: TranscodeStartupRetryAccelChange, stderr: av1DecoderHasNoGPU, keepRunning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetDeviceLoad(t)
			fakeDeviceStat(t, qsvPairDevA)
			logDir := t.TempDir()
			opts := TranscodeOpts{
				InputPath: "/nonexistent/input.mkv", OutputDir: t.TempDir(), SessionID: "decode-reject",
				SourceVideoCodec: tt.codec, SourceVideoProfile: "Main", SourceVideoBitDepth: tt.bitDepth,
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, TargetResolution: "1080p",
				FFmpegPath: gpuDecodeRejectingFFmpeg(t, logDir, tt.stderr, tt.keepRunning),
				HWAccel:    tt.hwAccel, HWDevice: qsvPairDevA,
			}
			pipeline := newAutoTranscodePipeline(context.Background(), opts, newAutoTranscodePipelineCache())
			if pipeline.Enabled() {
				t.Fatalf("explicit %s must not enable the auto pipeline", tt.hwAccel)
			}

			const startupWait = 10 * time.Second
			began := time.Now()
			session, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: startupWait, LegacyRetry: tt.policy})
			if err != nil {
				t.Fatalf("StartReadyTranscode: %v", err)
			}
			t.Cleanup(func() { _ = session.Close() })
			if elapsed := time.Since(began); elapsed >= startupWait/2 {
				t.Fatalf("startup took %s; the rejected GPU decode was not stopped early", elapsed)
			}
			if tt.keepRunning {
				assertProcessGone(t, filepath.Join(logDir, "gpu-child.pid"))
			}

			first := readFFmpegInvocation(t, logDir, 1)
			second := readFFmpegInvocation(t, logDir, 2)
			if !strings.Contains(first, "\n-hwaccel\n") {
				t.Fatalf("first attempt did not decode on the GPU:\n%s", first)
			}
			if strings.Contains(second, "\n-hwaccel\n") {
				t.Fatalf("retry decoded on the GPU again:\n%s", second)
			}
			if got := session.Opts(); got.HWAccel != tt.hwAccel || !got.SoftwareVideoDecode {
				t.Fatalf("running attempt = %s with CPU decode %v, want %s with CPU decode", got.HWAccel, got.SoftwareVideoDecode, tt.hwAccel)
			}
			if _, err := os.Stat(filepath.Join(logDir, "args-3")); err == nil {
				t.Fatal("startup ran a third attempt after the CPU-decode retry produced a manifest")
			}
		})
	}
}

// assertProcessGone fails unless the process whose pid pidFile holds has
// exited. Where nothing reaps orphans, a killed child stays a zombie, which
// procgrouptest.RequireExited counts as exited.
func assertProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(pidFile), err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", data, err)
	}
	procgrouptest.RequireExited(t, pid)
}

// Windows Media Video 7 and 8 and the Microsoft MPEG-4 variants have no GPU
// decoder in FFmpeg, so they are planned on CPU decode. WMV3 is VC-1, which
// NVIDIA and most Intel GPUs decode, and keeps GPU decode.
func TestRequiresSoftwareVideoDecodeForCodecsWithoutGPUDecoder(t *testing.T) {
	for codec, want := range map[string]bool{
		"wmv1": true, "wmv2": true, " WMV2 ": true, "msmpeg4v1": true, "msmpeg4v2": true, "msmpeg4v3": true, "msmpeg4": true,
		"wmv3": false, "vc1": false, "h264": false, "hevc": false, "av1": false, "": false,
	} {
		if got := RequiresSoftwareVideoDecode(codec, "", 8); got != want {
			t.Errorf("RequiresSoftwareVideoDecode(%q) = %v, want %v", codec, got, want)
		}
	}
}

// Only the AV1 decoder's own line on a GPU-decode command stops FFmpeg.
// FFmpeg echoes input paths in other lines, and a file named after the
// message must not stop a transcode that is working.
func TestStderrWriterStopsOnlyOnAV1DecoderRejection(t *testing.T) {
	const decoderLine = "[av1 @ 0x5581c2a0] Your platform doesn't support hardware accelerated AV1 decoding.\n"
	tests := []struct {
		name     string
		hwaccel  bool
		line     string
		wantStop bool
	}{
		{name: "decoder line on GPU decode", hwaccel: true, line: decoderLine, wantStop: true},
		{name: "decoder line on CPU decode", line: decoderLine},
		{name: "input path naming the message", hwaccel: true,
			line: "/media/doesn't support hardware accelerated AV1 decoding.mkv: Invalid data found when processing input\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"-c", "sleep 30", "sh"}
			if tt.hwaccel {
				args = append(args, "-hwaccel", "vaapi")
			}
			cmd := procgroup.CommandContext(context.Background(), "/bin/sh", args...)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() { _ = cmd.Wait(); close(exited) }()
			t.Cleanup(func() { _ = procgroup.Kill(cmd); <-exited })

			writer := &ffmpegStderrWriter{session: &TranscodeSession{}, ctx: context.Background(), cmd: cmd}
			if _, err := writer.Write([]byte(tt.line)); err != nil {
				t.Fatal(err)
			}
			wait := 5 * time.Second
			if !tt.wantStop {
				wait = 200 * time.Millisecond
			}
			select {
			case <-exited:
				if !tt.wantStop {
					t.Fatal("FFmpeg was stopped by a line that is not the AV1 decoder's rejection on a GPU-decode command")
				}
			case <-time.After(wait):
				if tt.wantStop {
					t.Fatal("FFmpeg kept running after its AV1 decoder rejected the GPU")
				}
			}
		})
	}
}

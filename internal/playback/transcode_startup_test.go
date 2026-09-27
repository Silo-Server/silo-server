package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestStartReadyTranscodeAdvancesOnExitAndAvoidsFailedDevice(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), cache)
	var attempts []TranscodeOpts
	var outputDirs []string
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts = append(attempts, opts)
		selected := "gpu-0"
		if opts.AvoidHWDevice == selected {
			selected = "gpu-1"
		}
		opts.HWDevice = selected
		outputDir := t.TempDir()
		outputDirs = append(outputDirs, outputDir)
		return fakeStartupSession(t, opts, outputDir, len(attempts) == 2, false), nil
	}

	session, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
	if err != nil {
		t.Fatalf("StartReadyTranscode: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(attempts))
	}
	if attempts[1].AvoidHWDevice != "gpu-0" || !attempts[1].SoftwareVideoDecode || attempts[1].HWAccel != transcodeHWNVENC {
		t.Fatalf("fallback = %+v, want mixed NVENC path avoiding gpu-0", attempts[1])
	}
	if got := session.Opts().HWDevice; got != "gpu-1" {
		t.Fatalf("selected device = %q, want gpu-1", got)
	}
	if _, statErr := os.Stat(outputDirs[0]); !os.IsNotExist(statErr) {
		t.Fatalf("failed fresh attempt kept its output dir: %v", statErr)
	}
	if preferred, found := cache.get(pipeline.cacheKey); !found || preferred.stage != autoTranscodeMixed {
		t.Fatalf("cache = %+v (found %v), want mixed stage remembered", preferred, found)
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

func TestStartReconstructTranscodeKeepsSlowProcess(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), newAutoTranscodePipelineCache())
	outputDir := t.TempDir()
	attempts := 0
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts++
		return fakeStartupSession(t, opts, outputDir, false, true), nil
	}

	session, err := StartReconstructTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
	if err != nil {
		t.Fatalf("StartReconstructTranscode: %v", err)
	}
	if !session.IsRunning() {
		t.Fatal("slow reconstruct was stopped, want it kept running")
	}
	if attempts != 1 {
		t.Fatalf("attempt count = %d, want 1", attempts)
	}
	if _, statErr := os.Stat(outputDir); statErr != nil {
		t.Fatalf("output dir removed: %v", statErr)
	}
}

func TestStartReconstructTranscodeKeepsOutputForNextAttempt(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), newAutoTranscodePipelineCache())
	outputDir := t.TempDir()
	servedSegment := filepath.Join(outputDir, "seg_00010.ts")
	if err := os.WriteFile(servedSegment, []byte("segment"), 0o600); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts++
		if _, err := os.Stat(servedSegment); err != nil {
			t.Fatalf("attempt %d: failed attempt removed the shared output dir: %v", attempts, err)
		}
		return fakeStartupSession(t, opts, outputDir, attempts == 3, false), nil
	}

	session, err := StartReconstructTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
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

func TestStartReconstructTranscodeFinalFailureClosesSession(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), newAutoTranscodePipelineCache())
	outputDir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts++
		if _, err := os.Stat(outputDir); err != nil {
			t.Fatalf("attempt %d: output dir removed before the final failure: %v", attempts, err)
		}
		return fakeStartupSession(t, opts, outputDir, false, false), nil
	}

	_, err := StartReconstructTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Millisecond, Start: start})
	var startupErr *TranscodeStartupError
	if !errors.As(err, &startupErr) {
		t.Fatalf("error = %v, want readiness failure", err)
	}
	if attempts != 3 {
		t.Fatalf("attempt count = %d, want 3", attempts)
	}
	if _, statErr := os.Stat(outputDir); !os.IsNotExist(statErr) {
		t.Fatalf("final reconstruct failure kept its output dir: %v", statErr)
	}
}

func TestStartReconstructTranscodeWithoutFallbackDoesNotWait(t *testing.T) {
	base := startupTestOpts()
	base.HWAccel = HWAccelNone
	pipeline := newAutoTranscodePipeline(context.Background(), base, newAutoTranscodePipelineCache())
	attempts := 0
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts++
		return fakeStartupSession(t, opts, t.TempDir(), false, true), nil
	}

	began := time.Now()
	session, err := StartReconstructTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: time.Minute, Start: start})
	if err != nil {
		t.Fatalf("StartReconstructTranscode: %v", err)
	}
	if elapsed := time.Since(began); elapsed > 10*time.Second {
		t.Fatalf("reconstruct without fallback waited %s for a manifest", elapsed)
	}
	if attempts != 1 || session == nil || !session.IsRunning() {
		t.Fatalf("attempts = %d, session = %v; want the single started process", attempts, session)
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

func TestStartReconstructTranscodeOutlivesTriggeringRequest(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), newAutoTranscodePipelineCache())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		return fakeStartupSession(t, opts, t.TempDir(), false, true), nil
	}

	session, err := StartReconstructTranscode(ctx, pipeline, TranscodeStartup{Timeout: 50 * time.Millisecond, Start: start})
	if err != nil {
		t.Fatalf("StartReconstructTranscode: %v", err)
	}
	if !session.IsRunning() {
		t.Fatal("reconstruct was stopped with its triggering request, want it kept for other waiters")
	}
}

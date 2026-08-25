package playback

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type hwAccelTestEnv struct {
	devDir string
	driDir string
	sysDir string
}

type fakeFFmpegProbe struct {
	cuda       bool
	h264NVENC  bool
	hevcNVENC  bool
	scaleCUDA  bool
	uploadCUDA bool
	smokeOK    bool
	hang       bool
	delay      time.Duration
}

type fakeFFmpegBinary struct {
	path    string
	logPath string
}

func TestResolveHWAccelWithFFmpegAutoPrefersNVENCOverIntel(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path); got != "nvenc" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want nvenc", got)
	}
}

func TestResolveHWAccelWithFFmpegFallsBackToIntelWhenNVENCProbeFails(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{})

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path); got != "qsv" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want qsv", got)
	}
}

func TestResolveHWAccelWithFFmpegFallsBackToVAAPIWhenNVENCProbeFails(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	env.addRenderDevice(t, "renderD129", "0x1002")
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{})

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path); got != "vaapi" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want vaapi", got)
	}
}

func TestResolveHWAccelWithFFmpegReturnsNoneWhenNVENCProbeFailsWithoutFallback(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{})

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path); got != "none" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want none", got)
	}
}

func TestResolveHWAccelWithFFmpegUsesNVIDIADeviceNodesWithoutDRM(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addNVIDIADevice(t, "nvidia0")
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path); got != "nvenc" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want nvenc", got)
	}
}

func TestExplicitNVENCBypassesFFmpegProbe(t *testing.T) {
	setupHWAccelTest(t)

	if got := ResolveHWAccelWithFFmpeg("nvenc", "/does/not/exist/ffmpeg"); got != "nvenc" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want nvenc", got)
	}
}

func TestResolveHWAccelWithFFmpegContextHonorsCallerDeadline(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{hang: true})

	// 60ms, not the caller deadline's natural handful of milliseconds: the
	// budget has to cover the fake sysfs walk in listRenderDevices before the
	// probe is reached, which is cold when this test runs after the rest of the
	// package. Too tight and exec never starts the process, so nothing is
	// logged. It stays far below the 200ms per-command timeout the assertion
	// below distinguishes it from.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	started := time.Now()
	got := ResolveHWAccelWithFFmpegContext(ctx, "auto", ffmpeg.path)
	cancel()
	if got != HWAccelNone {
		t.Fatalf("ResolveHWAccelWithFFmpegContext() = %q, want none", got)
	}
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("caller deadline took %s, want less than per-command timeout", elapsed)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
	}

	retryCtx, retryCancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	_ = ResolveHWAccelWithFFmpegContext(retryCtx, "auto", ffmpeg.path)
	retryCancel()
	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(logData), "\n"); calls < 1 || calls > 4 {
		t.Fatalf("canceled probe command count = %d, want one shared attempt of at most four commands", calls)
	}
}

func TestNormalizeProbeRequestTimeout(t *testing.T) {
	for _, test := range []struct {
		name     string
		millis   int64
		fallback time.Duration
		want     time.Duration
	}{
		{name: "missing uses caller fallback", fallback: 2 * time.Minute, want: 2 * time.Minute},
		{name: "negative uses caller fallback", millis: -1, fallback: 2 * time.Minute, want: 2 * time.Minute},
		{name: "too small", millis: time.Second.Milliseconds(), fallback: 2 * time.Minute, want: 5 * time.Second},
		{name: "advertised", millis: (137 * time.Second).Milliseconds(), fallback: 2 * time.Minute, want: 137 * time.Second},
		{name: "too large", millis: (10 * time.Minute).Milliseconds(), fallback: 2 * time.Minute, want: 5 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeProbeRequestTimeout(test.millis, test.fallback); got != test.want {
				t.Fatalf("NormalizeProbeRequestTimeout() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestResolveFFmpegPathTrimsWithoutCleaningRelativeExecutable(t *testing.T) {
	if got := ResolveFFmpegPath(" ./ffmpeg "); got != "./ffmpeg" {
		t.Fatalf("ResolveFFmpegPath() = %q, want ./ffmpeg", got)
	}
}

func TestFFmpegSupportsNVENCRequiresCUDAEncodersFiltersAndSmoke(t *testing.T) {
	setupHWAccelTest(t)
	tests := []struct {
		name  string
		probe fakeFFmpegProbe
	}{
		{
			name: "missing cuda hwaccel",
			probe: fakeFFmpegProbe{
				h264NVENC: true, hevcNVENC: true, scaleCUDA: true, uploadCUDA: true, smokeOK: true,
			},
		},
		{
			name: "missing h264 nvenc encoder",
			probe: fakeFFmpegProbe{
				cuda: true, hevcNVENC: true, scaleCUDA: true, uploadCUDA: true, smokeOK: true,
			},
		},
		{
			name: "missing hevc nvenc encoder",
			probe: fakeFFmpegProbe{
				cuda: true, h264NVENC: true, scaleCUDA: true, uploadCUDA: true, smokeOK: true,
			},
		},
		{
			name: "missing scale cuda filter",
			probe: fakeFFmpegProbe{
				cuda: true, h264NVENC: true, hevcNVENC: true, uploadCUDA: true, smokeOK: true,
			},
		},
		{
			name: "missing hwupload cuda filter",
			probe: fakeFFmpegProbe{
				cuda: true, h264NVENC: true, hevcNVENC: true, scaleCUDA: true, smokeOK: true,
			},
		},
		{
			name: "smoke encode failure",
			probe: fakeFFmpegProbe{
				cuda: true, h264NVENC: true, hevcNVENC: true, scaleCUDA: true, uploadCUDA: true,
			},
		},
		{
			name:  "probe timeout",
			probe: fakeFFmpegProbe{hang: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetNVENCProbeCacheForTest()
			ffmpeg := writeFakeFFmpeg(t, tt.probe)
			if ok, reason := ffmpegSupportsNVENC(ffmpeg.path); ok {
				t.Fatalf("ffmpegSupportsNVENC() = true, want false")
			} else if reason == "" {
				t.Fatalf("ffmpegSupportsNVENC() reason is empty")
			}
		})
	}
}

func TestFFmpegSupportsNVENCCachesByFFmpegPath(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())

	for i := 0; i < 2; i++ {
		if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path); got != "nvenc" {
			t.Fatalf("ResolveHWAccelWithFFmpeg() call %d = %q, want nvenc", i+1, got)
		}
	}

	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatalf("read ffmpeg probe log: %v", err)
	}
	if got := strings.Count(string(logData), "\n"); got != 4 {
		t.Fatalf("probe command count = %d, want 4; log:\n%s", got, logData)
	}
}

func TestFFmpegSupportsNVENCCoalescesConcurrentColdProbes(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	probe := successfulNVENCProbe()
	probe.delay = 30 * time.Millisecond
	ffmpeg := writeFakeFFmpeg(t, probe)

	start := make(chan struct{})
	results := make(chan string, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- ResolveHWAccelWithFFmpeg("auto", ffmpeg.path)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if result != "nvenc" {
			t.Fatalf("concurrent resolution = %q, want nvenc", result)
		}
	}

	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatalf("read ffmpeg probe log: %v", err)
	}
	if got := strings.Count(string(logData), "\n"); got != 4 {
		t.Fatalf("probe command count = %d, want one four-command shared probe; log:\n%s", got, logData)
	}
}

func TestFFmpegSupportsNVENCInvalidatesWhenBinaryChangesInPlace(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())
	if ok, reason := ffmpegSupportsNVENC(ffmpeg.path); !ok {
		t.Fatalf("initial NVENC probe failed: %s", reason)
	}

	replacement := "#!/bin/sh\necho 'replacement without NVENC support'\nexit 0\n"
	if err := os.WriteFile(ffmpeg.path, []byte(replacement), 0o755); err != nil {
		t.Fatalf("replace fake FFmpeg: %v", err)
	}
	changedAt := time.Now().Add(time.Second)
	if err := os.Chtimes(ffmpeg.path, changedAt, changedAt); err != nil {
		t.Fatalf("advance replacement timestamp: %v", err)
	}
	if ok, _ := ffmpegSupportsNVENC(ffmpeg.path); ok {
		t.Fatal("replaced FFmpeg binary reused a stale positive NVENC result")
	}
}

func TestNVENCProbeCacheKeyIncludesResolvedPATHIdentity(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	stamp := time.Unix(100, 0)
	for _, dir := range []string{firstDir, secondDir} {
		path := filepath.Join(dir, "ffmpeg")
		if err := os.WriteFile(path, []byte("same-binary-shape"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("PATH", firstDir)
	firstKey := nvencProbeCacheKey("ffmpeg")
	t.Setenv("PATH", secondDir)
	secondKey := nvencProbeCacheKey("ffmpeg")

	if firstKey == secondKey {
		t.Fatalf("PATH-resolved FFmpeg identities collided: %q", firstKey)
	}
}

func TestFFmpegSupportsNVENCNegativeResultExpires(t *testing.T) {
	setupHWAccelTest(t)
	oldTTL := nvencProbeNegativeTTL
	nvencProbeNegativeTTL = 20 * time.Millisecond
	t.Cleanup(func() { nvencProbeNegativeTTL = oldTTL })

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	markerPath := filepath.Join(dir, "nvenc-ready")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *-hwaccels*) echo cuda; exit 0 ;;
  *-encoders*) echo 'h264_nvenc hevc_nvenc'; exit 0 ;;
  *-filters*) echo 'scale_cuda hwupload_cuda'; exit 0 ;;
  *) test -e %q ;;
esac
`, markerPath)
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write marker-controlled FFmpeg: %v", err)
	}

	if ok, _ := ffmpegSupportsNVENC(ffmpegPath); ok {
		t.Fatal("initial NVENC probe unexpectedly succeeded")
	}
	if err := os.WriteFile(markerPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("enable NVENC smoke probe: %v", err)
	}
	if ok, _ := ffmpegSupportsNVENC(ffmpegPath); ok {
		t.Fatal("negative result was not retained during its short TTL")
	}
	time.Sleep(2 * nvencProbeNegativeTTL)
	if ok, reason := ffmpegSupportsNVENC(ffmpegPath); !ok {
		t.Fatalf("expired negative result was not retried: %s", reason)
	}
}

func TestFFmpegSupportsNVENCSmokeProbeUsesSafeFrameDimensions(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())

	if ok, reason := ffmpegSupportsNVENC(ffmpeg.path); !ok {
		t.Fatalf("ffmpegSupportsNVENC() = false, want true (reason=%q)", reason)
	}

	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatalf("read ffmpeg probe log: %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "testsrc2=size=640x360:rate=1") {
		t.Fatalf("smoke probe should use 640x360 input; log:\n%s", logText)
	}
}

func successfulNVENCProbe() fakeFFmpegProbe {
	return fakeFFmpegProbe{
		cuda:       true,
		h264NVENC:  true,
		hevcNVENC:  true,
		scaleCUDA:  true,
		uploadCUDA: true,
		smokeOK:    true,
	}
}

func setupHWAccelTest(t *testing.T) *hwAccelTestEnv {
	t.Helper()

	oldDRIDir := defaultDRIDir
	oldNVIDIAControlDevice := defaultNVIDIAControlDevice
	oldNVIDIADeviceGlob := defaultNVIDIADeviceGlob
	oldSysClassDRMDir := sysClassDRMDir
	oldGOOS := currentGOOS
	oldProbeTimeout := nvencProbeCommandTimeout
	resetNVENCProbeCacheForTest()

	tmp := t.TempDir()
	env := &hwAccelTestEnv{
		devDir: filepath.Join(tmp, "dev"),
		driDir: filepath.Join(tmp, "dev", "dri"),
		sysDir: filepath.Join(tmp, "sys", "class", "drm"),
	}
	defaultDRIDir = env.driDir
	defaultNVIDIAControlDevice = filepath.Join(env.devDir, "nvidiactl")
	defaultNVIDIADeviceGlob = filepath.Join(env.devDir, "nvidia[0-9]*")
	sysClassDRMDir = env.sysDir
	currentGOOS = "linux"
	nvencProbeCommandTimeout = 200 * time.Millisecond

	if err := os.MkdirAll(env.driDir, 0o755); err != nil {
		t.Fatalf("create test dri dir: %v", err)
	}
	if err := os.MkdirAll(env.devDir, 0o755); err != nil {
		t.Fatalf("create test dev dir: %v", err)
	}

	t.Cleanup(func() {
		defaultDRIDir = oldDRIDir
		defaultNVIDIAControlDevice = oldNVIDIAControlDevice
		defaultNVIDIADeviceGlob = oldNVIDIADeviceGlob
		sysClassDRMDir = oldSysClassDRMDir
		currentGOOS = oldGOOS
		nvencProbeCommandTimeout = oldProbeTimeout
		resetNVENCProbeCacheForTest()
	})

	return env
}

func (e *hwAccelTestEnv) addRenderDevice(t *testing.T, name string, vendor string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.driDir, name), []byte{}, 0o600); err != nil {
		t.Fatalf("create render device: %v", err)
	}
	vendorPath := filepath.Join(e.sysDir, name, "device", "vendor")
	if err := os.MkdirAll(filepath.Dir(vendorPath), 0o755); err != nil {
		t.Fatalf("create vendor dir: %v", err)
	}
	if err := os.WriteFile(vendorPath, []byte(vendor+"\n"), 0o644); err != nil {
		t.Fatalf("write vendor file: %v", err)
	}
}

func (e *hwAccelTestEnv) addNVIDIADevice(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.devDir, name), []byte{}, 0o600); err != nil {
		t.Fatalf("create nvidia device: %v", err)
	}
}

func writeFakeFFmpeg(t *testing.T, probe fakeFFmpegProbe) fakeFFmpegBinary {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ffmpeg")
	logPath := filepath.Join(dir, "probe.log")

	script := "#!/bin/sh\n"
	script += fmt.Sprintf("printf '%%s\\n' \"$*\" >> %q\n", logPath)
	if probe.delay > 0 {
		script += fmt.Sprintf("sleep %.3f\n", probe.delay.Seconds())
	}
	if probe.hang {
		script += "exec sleep 2147483647\n"
	}
	script += "case \"$*\" in\n"
	script += "  *-hwaccels*)\n"
	script += "    echo 'Hardware acceleration methods:'\n"
	if probe.cuda {
		script += "    echo 'cuda'\n"
	}
	script += "    exit 0 ;;\n"
	script += "  *-encoders*)\n"
	if probe.h264NVENC {
		script += "    echo ' V..... h264_nvenc NVIDIA NVENC H.264 encoder'\n"
	}
	if probe.hevcNVENC {
		script += "    echo ' V..... hevc_nvenc NVIDIA NVENC hevc encoder'\n"
	}
	script += "    exit 0 ;;\n"
	script += "  *-filters*)\n"
	if probe.scaleCUDA {
		script += "    echo ' ... scale_cuda V->V GPU video scaling'\n"
	}
	if probe.uploadCUDA {
		script += "    echo ' ... hwupload_cuda V->V upload CUDA frames'\n"
	}
	script += "    exit 0 ;;\n"
	script += "  *)\n"
	if probe.smokeOK {
		script += "    exit 0 ;;\n"
	} else {
		script += "    echo 'no capable devices found' >&2\n"
		script += "    exit 1 ;;\n"
	}
	script += "esac\n"

	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return fakeFFmpegBinary{path: path, logPath: logPath}
}

func resetNVENCProbeCacheForTest() {
	nvencProbeCache.Lock()
	defer nvencProbeCache.Unlock()
	nvencProbeCache.byPath = make(map[string]nvencProbeCacheEntry)
}

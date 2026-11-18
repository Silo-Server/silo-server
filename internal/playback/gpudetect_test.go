package playback

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type hwAccelTestEnv struct {
	devDir string
	driDir string
	sysDir string
}

type fakeFFmpegProbe struct {
	cuda         bool
	qsvHWAccel   bool
	vaapiHWAccel bool
	h264NVENC    bool
	hevcNVENC    bool
	h264QSV      bool
	hevcQSV      bool
	h264VAAPI    bool
	scaleCUDA    bool
	uploadCUDA   bool
	videotoolbox bool
	h264VT       bool
	hevcVT       bool
	smokeOK      bool
	// smokeFailures names encoders whose smoke encode fails even when smokeOK
	// is set, modeling a listed encoder with no working driver behind it.
	smokeFailures []string
	// smokeDeviceFailures names render devices (by basename) whose smoke encode
	// fails, modeling one broken GPU on a host that has another working one.
	smokeDeviceFailures []string
	hang                bool
	delay               time.Duration
}

type fakeFFmpegBinary struct {
	path    string
	logPath string
}

// A proxy node reads the cluster-wide hw_device meant for the transcode nodes:
// the paths and their sysfs vendor entries are visible, but the devices cannot
// be opened. Detection must skip the probes entirely — no ffmpeg spawn, no
// alarming driver error — and say why.
func TestDetectHWAccelSkipsConfiguredDevicesItCannotOpen(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x8086")
	configured := filepath.Join(env.driDir, "renderD128") + "," + filepath.Join(env.driDir, "renderD129")
	for _, name := range []string{"renderD128", "renderD129"} {
		if err := os.Remove(filepath.Join(env.driDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	info := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, configured)
	if info.Resolved != HWAccelNone {
		t.Fatalf("Resolved = %q, want none", info.Resolved)
	}
	if len(info.DetectedBackends) == 0 {
		t.Fatal("DetectedBackends is empty, want skipped qsv/vaapi entries")
	}
	for _, backend := range info.DetectedBackends {
		if !backend.Skipped {
			t.Fatalf("backend %q Skipped = false, want true", backend.Backend)
		}
		if backend.Verified {
			t.Fatalf("backend %q Verified = true, want false", backend.Backend)
		}
		if !strings.Contains(backend.Reason, "not accessible") {
			t.Fatalf("backend %q Reason = %q, want an accessibility reason", backend.Backend, backend.Reason)
		}
	}
	if logData, err := os.ReadFile(ffmpeg.logPath); err == nil && len(strings.TrimSpace(string(logData))) > 0 {
		t.Fatalf("ffmpeg was spawned for inaccessible devices; log:\n%s", logData)
	}
}

// One configured device is gone, the other works: the accessible one must
// still be probed and win, and the missing one must not be smoke-encoded.
func TestDetectHWAccelProbesOnlyTheAccessibleConfiguredDevices(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x8086")
	if err := os.Remove(filepath.Join(env.driDir, "renderD128")); err != nil {
		t.Fatal(err)
	}
	configured := filepath.Join(env.driDir, "renderD128") + "," + filepath.Join(env.driDir, "renderD129")
	ffmpeg := writeFakeFFmpeg(t, successfulQSVProbe())

	info := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, configured)
	if info.Resolved != "qsv" {
		t.Fatalf("Resolved = %q, want qsv from the accessible device", info.Resolved)
	}
	var qsv *DetectedBackend
	for i := range info.DetectedBackends {
		if info.DetectedBackends[i].Backend == "qsv" {
			qsv = &info.DetectedBackends[i]
		}
	}
	if qsv == nil || qsv.Skipped || !qsv.Verified {
		t.Fatalf("qsv entry = %+v, want verified and not skipped", qsv)
	}
	if qsv.Device != filepath.Join(env.driDir, "renderD129") {
		t.Fatalf("qsv Device = %q, want the accessible renderD129", qsv.Device)
	}
	logData := fakeFFmpegLog(t, ffmpeg)
	if strings.Contains(string(logData), filepath.Join(env.driDir, "renderD128")) {
		t.Fatalf("probe touched the inaccessible device; log:\n%s", logData)
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
	got := ResolveHWAccelWithFFmpegContext(ctx, "auto", ffmpeg.path, "")
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
	_ = ResolveHWAccelWithFFmpegContext(retryCtx, "auto", ffmpeg.path, "")
	retryCancel()
	logData := fakeFFmpegLog(t, ffmpeg)
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
		{
			// The ceiling is derived from the probe formula rather than picked,
			// so the assertion is too.
			name:     "too large",
			millis:   (24 * time.Hour).Milliseconds(),
			fallback: 2 * time.Minute,
			want:     MaxCapabilityRequestTimeout(),
		},
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

func TestResolveFFmpegPathFallsBackOnlyForMissingLegacyDefault(t *testing.T) {
	missing := func(string) (string, error) { return "", fmt.Errorf("not found") }
	discover := func() string { return homebrewFFmpegFullAppleSilicon }

	if got := resolveFFmpegPath(jellyfinFFmpegPath, missing, discover); got != discover() {
		t.Fatalf("legacy default resolved to %q, want discovery result", got)
	}
	if got := resolveFFmpegPath("/custom/missing/ffmpeg", missing, discover); got != "/custom/missing/ffmpeg" {
		t.Fatalf("custom path resolved to %q, want the explicit value preserved", got)
	}
}

func TestHWProbeCacheSeparatesBackendsAndDevices(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	keys := map[string]string{
		"nvenc":       hwProbeCacheKey(0, ffmpeg.path, transcodeHWNVENC, ""),
		"qsv-128":     hwProbeCacheKey(0, ffmpeg.path, transcodeHWQSV, "/dev/dri/renderD128"),
		"qsv-129":     hwProbeCacheKey(0, ffmpeg.path, transcodeHWQSV, "/dev/dri/renderD129"),
		"vaapi-128":   hwProbeCacheKey(0, ffmpeg.path, transcodeHWVAAPI, "/dev/dri/renderD128"),
		"identity-eq": hwProbeCacheKey(0, ffmpeg.path, transcodeHWNVENC, ""),
	}
	if keys["nvenc"] != keys["identity-eq"] {
		t.Fatal("identical backend and device produced different cache keys")
	}
	seen := map[string]string{}
	for name, key := range keys {
		if name == "identity-eq" {
			continue
		}
		if other, ok := seen[key]; ok {
			t.Fatalf("cache keys for %s and %s collided", name, other)
		}
		seen[key] = name
	}

	// Each distinct key runs its own probe command set: 3 for QSV on two
	// devices, 2 for VAAPI.
	for _, device := range []string{"/dev/dri/renderD128", "/dev/dri/renderD129"} {
		if ok, reason := ffmpegSupportsBackend(transcodeHWQSV, ffmpeg.path, device); !ok {
			t.Fatalf("QSV probe on %s failed: %s", device, reason)
		}
	}
	if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpeg.path, "/dev/dri/renderD128"); !ok {
		t.Fatalf("VAAPI probe failed: %s", reason)
	}
	logData := fakeFFmpegLog(t, ffmpeg)
	if got := strings.Count(string(logData), "\n"); got != 8 {
		t.Fatalf("probe command count = %d, want 8 across three distinct cache keys; log:\n%s", got, logData)
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
			results <- ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, "")
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

	logData := fakeFFmpegLog(t, ffmpeg)
	if got := strings.Count(string(logData), "\n"); got != 4 {
		t.Fatalf("probe command count = %d, want one four-command shared probe; log:\n%s", got, logData)
	}
}

func TestFFmpegSupportsNVENCInvalidatesWhenBinaryChangesInPlace(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())
	if ok, reason := ffmpegSupportsBackend(transcodeHWNVENC, ffmpeg.path, ""); !ok {
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
	if ok, _ := ffmpegSupportsBackend(transcodeHWNVENC, ffmpeg.path, ""); ok {
		t.Fatal("replaced FFmpeg binary reused a stale positive NVENC result")
	}
}

func TestFFmpegIdentityKeyIncludesResolvedPATHIdentity(t *testing.T) {
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
	firstKey := ffmpegIdentityKey("ffmpeg")
	t.Setenv("PATH", secondDir)
	secondKey := ffmpegIdentityKey("ffmpeg")

	if firstKey == secondKey {
		t.Fatalf("PATH-resolved FFmpeg identities collided: %q", firstKey)
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

func successfulQSVProbe() fakeFFmpegProbe {
	return fakeFFmpegProbe{
		qsvHWAccel: true,
		h264QSV:    true,
		hevcQSV:    true,
		smokeOK:    true,
	}
}

func successfulVAAPIProbe() fakeFFmpegProbe {
	return fakeFFmpegProbe{
		vaapiHWAccel: true,
		h264VAAPI:    true,
		smokeOK:      true,
	}
}

func fullyCapableProbe() fakeFFmpegProbe {
	return fakeFFmpegProbe{
		cuda:         true,
		qsvHWAccel:   true,
		vaapiHWAccel: true,
		h264NVENC:    true,
		hevcNVENC:    true,
		h264QSV:      true,
		hevcQSV:      true,
		h264VAAPI:    true,
		scaleCUDA:    true,
		uploadCUDA:   true,
		smokeOK:      true,
	}
}

func setupHWAccelTest(t *testing.T) *hwAccelTestEnv {
	t.Helper()

	oldDRIDir := defaultDRIDir
	oldNVIDIAControlDevice := defaultNVIDIAControlDevice
	oldNVIDIADeviceGlob := defaultNVIDIADeviceGlob
	oldDXGDevice := defaultDXGDevice
	oldSysClassDRMDir := sysClassDRMDir
	oldGOOS := currentGOOS
	oldProbeTimeout := hwProbeCommandTimeout
	oldProbeNow := hwProbeNow
	resetHWProbeCacheForTest()

	tmp := t.TempDir()
	env := &hwAccelTestEnv{
		devDir: filepath.Join(tmp, "dev"),
		driDir: filepath.Join(tmp, "dev", "dri"),
		sysDir: filepath.Join(tmp, "sys", "class", "drm"),
	}
	defaultDRIDir = env.driDir
	defaultNVIDIAControlDevice = filepath.Join(env.devDir, "nvidiactl")
	defaultNVIDIADeviceGlob = filepath.Join(env.devDir, "nvidia[0-9]*")
	defaultDXGDevice = filepath.Join(env.devDir, "dxg")
	sysClassDRMDir = env.sysDir
	currentGOOS = "linux"
	hwProbeCommandTimeout = 200 * time.Millisecond

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
		defaultDXGDevice = oldDXGDevice
		sysClassDRMDir = oldSysClassDRMDir
		currentGOOS = oldGOOS
		hwProbeCommandTimeout = oldProbeTimeout
		hwProbeNow = oldProbeNow
		resetHWProbeCacheForTest()
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
	writeFakeFFmpegScript(t, path, logPath, probe)
	return fakeFFmpegBinary{path: path, logPath: logPath}
}

func writeFakeFFmpegScript(t *testing.T, path, logPath string, probe fakeFFmpegProbe) {
	t.Helper()

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
	if probe.qsvHWAccel {
		script += "    echo 'qsv'\n"
	}
	if probe.vaapiHWAccel {
		script += "    echo 'vaapi'\n"
	}
	if probe.videotoolbox {
		script += "    echo 'videotoolbox'\n"
	}
	script += "    exit 0 ;;\n"
	script += "  *-encoders*)\n"
	if probe.h264NVENC {
		script += "    echo ' V..... h264_nvenc NVIDIA NVENC H.264 encoder'\n"
	}
	if probe.hevcNVENC {
		script += "    echo ' V..... hevc_nvenc NVIDIA NVENC hevc encoder'\n"
	}
	if probe.h264QSV {
		script += "    echo ' V..... h264_qsv H.264 QSV encoder'\n"
	}
	if probe.hevcQSV {
		script += "    echo ' V..... hevc_qsv HEVC QSV encoder'\n"
	}
	if probe.h264VAAPI {
		script += "    echo ' V..... h264_vaapi H.264 VAAPI encoder'\n"
	}
	if probe.h264VT {
		script += "    echo ' V..... h264_videotoolbox VideoToolbox H.264 encoder'\n"
	}
	if probe.hevcVT {
		script += "    echo ' V..... hevc_videotoolbox VideoToolbox hevc encoder'\n"
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
	for _, encoder := range []string{"h264_nvenc", "h264_qsv", "h264_vaapi", "h264_videotoolbox", "hevc_videotoolbox"} {
		script += fmt.Sprintf("  *%s*)\n", encoder)
		if probe.smokeOK && !slices.Contains(probe.smokeFailures, encoder) {
			// The init chain carries the device path, so a broken GPU is modeled
			// by matching the command rather than by ignoring the argument.
			if len(probe.smokeDeviceFailures) > 0 {
				patterns := make([]string, 0, len(probe.smokeDeviceFailures))
				for _, device := range probe.smokeDeviceFailures {
					patterns = append(patterns, "*"+device+"*")
				}
				script += "    case \"$*\" in\n"
				script += fmt.Sprintf("      %s)\n", strings.Join(patterns, "|"))
				script += fmt.Sprintf("        echo 'no capable devices found for %s' >&2\n", encoder)
				script += "        exit 1 ;;\n"
				script += "    esac\n"
			}
			script += "    exit 0 ;;\n"
		} else {
			script += fmt.Sprintf("    echo 'no capable devices found for %s' >&2\n", encoder)
			script += "    exit 1 ;;\n"
		}
	}
	script += "  *)\n"
	script += "    echo 'unexpected probe command' >&2\n"
	script += "    exit 1 ;;\n"
	script += "esac\n"

	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
}

// fakeFFmpegLog returns the command lines a fake ffmpeg has logged, one per
// invocation.
func fakeFFmpegLog(t *testing.T, ffmpeg fakeFFmpegBinary) string {
	t.Helper()
	data, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatalf("read fake ffmpeg log: %v", err)
	}
	return string(data)
}

// resetHWProbeCacheForTest clears the probe cache between cases. It delegates
// to the exported invalidation so tests exercise the same seam the re-probe
// action uses rather than a second, drifting implementation.
func resetHWProbeCacheForTest() {
	InvalidateHWProbeCache()
}

func successfulVideoToolboxProbe() fakeFFmpegProbe {
	return fakeFFmpegProbe{videotoolbox: true, h264VT: true, hevcVT: true, smokeOK: true}
}

func TestResolveHWAccelWithFFmpegContextDarwinHonorsCallerDeadline(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{hang: true})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	started := time.Now()
	if got := ResolveHWAccelWithFFmpegContext(ctx, "auto", ffmpeg.path, ""); got != HWAccelNone {
		t.Fatalf("ResolveHWAccelWithFFmpegContext() = %q, want none", got)
	}
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("caller deadline took %s, want less than per-command timeout", elapsed)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
	}
	// Let the bounded shared probe finish before test cleanup restores globals.
	_ = cachedVideoToolboxProbe(ffmpeg.path)
}

func TestResolveHWAccelWithFFmpegDarwinAllowsH264OnlyVideoToolbox(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{videotoolbox: true, h264VT: true, smokeOK: true})

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "videotoolbox" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want videotoolbox for H.264-only Mac", got)
	}
	if ok, _ := videoToolboxSupportsTargetCodec(ffmpeg.path, "hevc"); ok {
		t.Fatal("HEVC target unexpectedly accepted without hevc_videotoolbox")
	}
}

func TestCachedVideoToolboxProbeCoalescesConcurrentColdProbes(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{hang: true})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cachedVideoToolboxProbe(ffmpeg.path)
		}()
	}
	wg.Wait()

	log := fakeFFmpegLog(t, ffmpeg)
	if got := strings.Count(string(log), "-hwaccels"); got != 1 {
		t.Fatalf("concurrent cold probes ran %d ffmpeg invocations, want 1 (coalesced):\n%s", got, log)
	}
}

func TestCachedVideoToolboxProbeRetriesNegativeResultsAfterExpiry(t *testing.T) {
	setupHWAccelTest(t)
	oldDelay := videoToolboxProbeRetryDelay
	videoToolboxProbeRetryDelay = 0
	t.Cleanup(func() { videoToolboxProbeRetryDelay = oldDelay })

	dir := t.TempDir()
	ffmpegPath, markerPath := filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "recovered")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
 *-hwaccels*) echo videotoolbox ;;
 *-encoders*) echo "h264_videotoolbox hevc_videotoolbox" ;;
 *) test -e %q ;;
esac
`, markerPath)
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if result := cachedVideoToolboxProbe(ffmpegPath); result.available {
		t.Fatal("failing fake should probe unavailable")
	}

	// Recovery changes only the marker; executable identity stays unchanged.
	if err := os.WriteFile(markerPath, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := cachedVideoToolboxProbe(ffmpegPath); !result.available {
		t.Fatalf("expired negative result should re-probe, got reason %q", result.reason)
	}
}

func TestCachedVideoToolboxProbeInvalidatesWhenExecutableIsReplaced(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, successfulVideoToolboxProbe())
	if result := cachedVideoToolboxProbe(ffmpeg.path); !result.available {
		t.Fatalf("capable fake should probe available, got %q", result.reason)
	}

	// Replace the binary at the same configured spelling. Its identity is part
	// of the cache key, so the old positive verdict must not survive.
	writeFakeFFmpegScript(t, ffmpeg.path, ffmpeg.logPath, fakeFFmpegProbe{})
	if result := cachedVideoToolboxProbe(ffmpeg.path); result.available {
		t.Fatal("replacement binary reused the previous executable's positive verdict")
	}
}

// The walk deadline has to cover the matrix it will actually run, which grows
// with the device set: every configured render device is probed for both QSV
// and VAAPI. A fixed thirty seconds marked a three-device host incomplete while
// every individual command was still inside its own budget, and
// /hw-capabilities then answered 503 for a node that was working.
func TestHWAccelWalkTimeoutScalesWithTheDeviceSet(t *testing.T) {
	one := hwCandidates{
		intel: []string{"/dev/dri/renderD128"},
		vaapi: []string{"/dev/dri/renderD128"},
	}
	three := hwCandidates{
		intel: []string{"/dev/dri/renderD128", "/dev/dri/renderD129", "/dev/dri/renderD130"},
		vaapi: []string{"/dev/dri/renderD128", "/dev/dri/renderD129", "/dev/dri/renderD130"},
	}

	oneDevice, threeDevices := hwAccelWalkTimeout(one), hwAccelWalkTimeout(three)
	if threeDevices <= oneDevice {
		t.Fatalf("three-device walk %v is not longer than the one-device %v", threeDevices, oneDevice)
	}

	// It covers what it will run: every command the matrix allows, at its own
	// per-command bound, plus spawn slack: three QSV and two VAAPI commands on
	// each of three devices at 3s apiece, plus 5s.
	const wantThree = 50 * time.Second
	if threeDevices != wantThree {
		t.Fatalf("three-device walk = %v, want %v", threeDevices, wantThree)
	}
	if threeDevices <= 30*time.Second {
		t.Fatalf("three-device walk = %v, which the old fixed 30s bound would have cut short", threeDevices)
	}

	// A host with nothing to probe still gets a usable, non-zero deadline.
	if got := hwAccelWalkTimeout(hwCandidates{}); got <= 0 {
		t.Fatalf("empty walk timeout = %v, want a positive bound", got)
	}
}

// The ceiling is clamped against on an API replica, which does not have the
// remote node's cards. Pricing a synthetic device list there classified the
// fabricated paths as VAAPI-only — no sysfs vendor to read — so the ceiling came
// out below what a node with a dozen Intel devices legitimately advertises, and
// the clamp then canceled that node before its own matrix could finish.
func TestMaxCapabilityRequestTimeoutDoesNotDependOnTheLocalHost(t *testing.T) {
	env := setupHWAccelTest(t)
	bare := MaxCapabilityRequestTimeout()

	// Same process, now with Intel cards present: classification would change if
	// the ceiling consulted sysfs at all.
	for i := range 4 {
		env.addRenderDevice(t, "renderD"+strconv.Itoa(128+i), "0x8086")
	}
	if got := MaxCapabilityRequestTimeout(); got != bare {
		t.Fatalf("ceiling moved from %v to %v when local hardware appeared", bare, got)
	}

	// And it covers the largest matrix the cap allows: a full set of Intel
	// devices, which is the classification that draws two backends per device.
	devices := make([]string, 0, tonemap.MaxProbedDevices)
	for i := range tonemap.MaxProbedDevices {
		name := "renderD" + strconv.Itoa(200+i)
		env.addRenderDevice(t, name, "0x8086")
		devices = append(devices, env.devicePath(name))
	}
	if advertised := CapabilityRequestTimeout(hwAccelAuto, strings.Join(devices, ",")); advertised > bare {
		t.Fatalf("a full Intel set advertises %v, above the %v ceiling that clamps it", advertised, bare)
	}
}

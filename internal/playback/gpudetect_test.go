package playback

import (
	"context"
	"encoding/json"
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

func TestResolveHWAccelWithFFmpegAutoPrefersNVENCOverIntel(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "nvenc" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want nvenc", got)
	}
}

func TestResolveHWAccelWithFFmpegFallsBackToIntelWhenNVENCProbeFails(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, successfulQSVProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "qsv" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want qsv", got)
	}
}

func TestResolveHWAccelWithFFmpegFallsBackToVAAPIWhenNVENCProbeFails(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	env.addRenderDevice(t, "renderD129", "0x1002")
	ffmpeg := writeFakeFFmpeg(t, successfulVAAPIProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "vaapi" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want vaapi", got)
	}
}

func TestResolveHWAccelWithFFmpegFallsBackToVAAPIWhenQSVListingFails(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	probe := successfulVAAPIProbe()
	probe.qsvHWAccel = true
	probe.h264QSV = true
	// hevc_qsv is missing, so the QSV listing gate rejects an Intel GPU that
	// VAAPI can still drive.
	ffmpeg := writeFakeFFmpeg(t, probe)

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "vaapi" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want vaapi", got)
	}
}

func TestResolveHWAccelWithFFmpegTriesEveryCandidateDevice(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x1002")
	env.addRenderDevice(t, "renderD129", "0x1002")
	probe := successfulVAAPIProbe()
	// The GPU that sorts first has no working driver; the second one does.
	probe.smokeDeviceFailures = []string{"renderD128"}
	ffmpeg := writeFakeFFmpeg(t, probe)

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "vaapi" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want vaapi from the working device", got)
	}
	info := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, "")
	vaapi := info.DetectedBackends[len(info.DetectedBackends)-1]
	if vaapi.Backend != "vaapi" || !vaapi.Verified {
		t.Fatalf("vaapi entry = %+v, want verified", vaapi)
	}
	if device := filepath.Base(vaapi.Device); device != "renderD129" {
		t.Fatalf("verified device = %q, want renderD129", device)
	}
}

func TestResolveHWAccelWithFFmpegSkipsNVIDIANodesAsVAAPIDevices(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	env.addRenderDevice(t, "renderD129", "0x1002")
	probe := successfulVAAPIProbe()
	// An NVIDIA render node has no libva driver; probing it would reject a
	// backend the AMD card can drive.
	probe.smokeDeviceFailures = []string{"renderD128"}
	ffmpeg := writeFakeFFmpeg(t, probe)

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "vaapi" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want vaapi from the AMD device", got)
	}
	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), "vaapi=hw:"+filepath.Join(env.driDir, "renderD128")) {
		t.Fatalf("VAAPI probe used the NVIDIA render node; log:\n%s", logData)
	}
}

func TestResolveHWAccelWithFFmpegProbesTheConfiguredHWDevice(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x8086")
	probe := successfulQSVProbe()
	probe.smokeDeviceFailures = []string{"renderD128"}
	ffmpeg := writeFakeFFmpeg(t, probe)

	// The operator pinned the working GPU, which is the device a transcode
	// opens; auto resolution has to verify that one rather than renderD128.
	pinned := filepath.Join(env.driDir, "renderD129")
	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, pinned); got != "qsv" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want qsv on the pinned device", got)
	}
	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), filepath.Join(env.driDir, "renderD128")) {
		t.Fatalf("probe touched an unconfigured device; log:\n%s", logData)
	}
}

func TestDetectHWAccelReportsHostInventoryBehindAPinnedDevice(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x1002")
	ffmpeg := writeFakeFFmpeg(t, successfulVAAPIProbe())

	info := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, filepath.Join(env.driDir, "renderD129"))
	if info.Resolved != "vaapi" {
		t.Fatalf("Resolved = %q, want vaapi from the pinned AMD device", info.Resolved)
	}
	// Pinning a device narrows what is probed, never what is reported.
	if !info.IntelDetected {
		t.Fatal("IntelDetected = false, want the host's Intel GPU still reported")
	}
	if len(info.RenderDevices) != 2 {
		t.Fatalf("RenderDevices = %v, want the full host inventory", info.RenderDevices)
	}
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
	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), filepath.Join(env.driDir, "renderD128")) {
		t.Fatalf("probe touched the inaccessible device; log:\n%s", logData)
	}
}

func TestResolveHWAccelWithFFmpegReturnsNoneWhenVAAPISmokeEncodeFails(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x1002")
	probe := successfulVAAPIProbe()
	probe.smokeFailures = []string{"h264_vaapi"}
	ffmpeg := writeFakeFFmpeg(t, probe)

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != HWAccelNone {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want none", got)
	}
}

func TestResolveHWAccelWithFFmpegReturnsNoneWhenNVENCProbeFailsWithoutFallback(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{})

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "none" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want none", got)
	}
}

func TestResolveHWAccelWithFFmpegUsesNVIDIADeviceNodesWithoutDRM(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addNVIDIADevice(t, "nvidia0")
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "nvenc" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want nvenc", got)
	}
}

func TestResolveHWAccelPassesThroughConfiguredBackends(t *testing.T) {
	setupHWAccelTest(t)

	for _, configured := range []string{"nvenc", "qsv", "vaapi", "none", "custom"} {
		t.Run(configured, func(t *testing.T) {
			if got := ResolveHWAccelWithFFmpeg(configured, "/does/not/exist/ffmpeg", ""); got != configured {
				t.Fatalf("ResolveHWAccelWithFFmpeg(%q) = %q, want unchanged", configured, got)
			}
		})
	}
}

// Windows rather than macOS: macOS has its own hardware path through
// VideoToolbox, so it is no longer a platform with nothing to probe.
func TestResolveHWAccelAutoIsNoneOnAPlatformWithNoHardwarePath(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	currentGOOS = windowsGOOS
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != HWAccelNone {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want none", got)
	}
	info := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, "")
	if info.Resolved != HWAccelNone {
		t.Fatalf("DetectHWAccelWithFFmpeg().Resolved = %q, want none", info.Resolved)
	}
	if len(info.DetectedBackends) != 0 {
		t.Fatalf("DetectHWAccelWithFFmpeg().DetectedBackends = %+v, want empty off Linux", info.DetectedBackends)
	}
	if _, err := os.Stat(ffmpeg.logPath); !os.IsNotExist(err) {
		t.Fatalf("off-Linux detection ran FFmpeg probes (stat err = %v)", err)
	}
}

func TestDetectHWAccelReportsEveryCandidateBackend(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x10de")
	probe := fullyCapableProbe()
	probe.h264NVENC = false
	probe.smokeFailures = []string{"h264_vaapi"}
	ffmpeg := writeFakeFFmpeg(t, probe)

	info := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, "")
	if info.Resolved != "qsv" {
		t.Fatalf("Resolved = %q, want qsv", info.Resolved)
	}
	if len(info.DetectedBackends) != 3 {
		t.Fatalf("DetectedBackends = %+v, want one entry per candidate backend", info.DetectedBackends)
	}
	// The NVIDIA render node carries no libva driver, so it is not a VAAPI
	// candidate even though it is a render device.
	want := []DetectedBackend{
		{Backend: "nvenc", Verified: false, Devices: []string{"/dev/dri/renderD129"}},
		{Backend: "qsv", Verified: true, Devices: []string{"/dev/dri/renderD128"}},
		{Backend: "vaapi", Verified: false, Devices: []string{"/dev/dri/renderD128"}},
	}
	for i, expected := range want {
		got := info.DetectedBackends[i]
		if got.Backend != expected.Backend || got.Verified != expected.Verified {
			t.Fatalf("DetectedBackends[%d] = %+v, want backend %q verified=%v", i, got, expected.Backend, expected.Verified)
		}
		if !slices.Equal(stripDevicePrefix(got.Devices), stripDevicePrefix(expected.Devices)) {
			t.Fatalf("DetectedBackends[%d].Devices = %v, want %v", i, got.Devices, expected.Devices)
		}
		if expected.Verified && got.Reason != "" {
			t.Fatalf("DetectedBackends[%d].Reason = %q, want empty for a verified backend", i, got.Reason)
		}
		if !expected.Verified && got.Reason == "" {
			t.Fatalf("DetectedBackends[%d].Reason is empty, want a failure explanation", i)
		}
	}
	if device := filepath.Base(info.DetectedBackends[1].Device); device != "renderD128" {
		t.Fatalf("qsv verified device = %q, want the Intel render node", device)
	}
	if reason := info.DetectedBackends[0].Reason; reason != "h264_nvenc encoder unavailable" {
		t.Fatalf("nvenc reason = %q, want the missing encoder", reason)
	}
	if reason := info.DetectedBackends[2].Reason; !strings.HasPrefix(reason, "h264_vaapi smoke encode failed") {
		t.Fatalf("vaapi reason = %q, want the failed smoke encode", reason)
	}
}

func TestDetectHWAccelOmitsBackendsWithoutCandidateHardware(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	info := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, "")
	backends := make([]string, 0, len(info.DetectedBackends))
	for _, entry := range info.DetectedBackends {
		backends = append(backends, entry.Backend)
	}
	if !slices.Equal(backends, []string{"qsv", "vaapi"}) {
		t.Fatalf("detected backends = %v, want qsv and vaapi only", backends)
	}
	if !info.IntelDetected {
		t.Fatal("IntelDetected = false, want true")
	}
}

func TestDetectedBackendJSONShape(t *testing.T) {
	encoded, err := json.Marshal(HWAccelInfo{
		Resolved: "qsv",
		DetectedBackends: []DetectedBackend{
			{Backend: "qsv", Verified: true, Devices: []string{"/dev/dri/renderD128"}},
			{Backend: "nvenc", Verified: false, Reason: "h264_nvenc encoder unavailable"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"detected_backends":[`,
		`{"backend":"qsv","verified":true,"devices":["/dev/dri/renderD128"]}`,
		`{"backend":"nvenc","verified":false,"reason":"h264_nvenc encoder unavailable"}`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("HWAccelInfo JSON = %s, missing %s", encoded, want)
		}
	}

	empty, err := json.Marshal(HWAccelInfo{Resolved: HWAccelNone})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "detected_backends") {
		t.Fatalf("HWAccelInfo JSON = %s, want detected_backends omitted when empty", empty)
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

func TestDiscoverFFmpegPathDarwinPrefersFullHomebrewBuilds(t *testing.T) {
	tests := []struct {
		name      string
		available map[string]bool
		want      string
	}{
		{
			name:      "Apple Silicon prefix",
			available: map[string]bool{homebrewFFmpegFullAppleSilicon: true},
			want:      homebrewFFmpegFullAppleSilicon,
		},
		{
			name:      "Intel prefix",
			available: map[string]bool{homebrewFFmpegFullIntel: true},
			want:      homebrewFFmpegFullIntel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookPath := func(path string) (string, error) {
				if tt.available[path] {
					return path, nil
				}
				return "", fmt.Errorf("not found")
			}
			if got := discoverFFmpegPath(darwinGOOS, lookPath); got != tt.want {
				t.Fatalf("discoverFFmpegPath() = %q, want %q", got, tt.want)
			}
		})
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
			resetHWProbeCacheForTest()
			ffmpeg := writeFakeFFmpeg(t, tt.probe)
			if ok, reason := ffmpegSupportsBackend(transcodeHWNVENC, ffmpeg.path, ""); ok {
				t.Fatalf("ffmpegSupportsBackend(nvenc) = true, want false")
			} else if reason == "" {
				t.Fatalf("ffmpegSupportsBackend(nvenc) reason is empty")
			}
		})
	}
}

func TestFFmpegSupportsQSVRequiresListingsAndSmoke(t *testing.T) {
	setupHWAccelTest(t)
	tests := []struct {
		name  string
		probe fakeFFmpegProbe
		want  string
	}{
		{
			name:  "missing qsv and vaapi hwaccels",
			probe: fakeFFmpegProbe{h264QSV: true, hevcQSV: true, smokeOK: true},
			want:  "qsv and vaapi hwaccels unavailable",
		},
		{
			name:  "missing h264 qsv encoder",
			probe: fakeFFmpegProbe{qsvHWAccel: true, hevcQSV: true, smokeOK: true},
			want:  "h264_qsv encoder unavailable",
		},
		{
			name:  "missing hevc qsv encoder",
			probe: fakeFFmpegProbe{qsvHWAccel: true, h264QSV: true, smokeOK: true},
			want:  "hevc_qsv encoder unavailable",
		},
		{
			name:  "smoke encode failure",
			probe: fakeFFmpegProbe{vaapiHWAccel: true, h264QSV: true, hevcQSV: true},
			want:  "h264_qsv smoke encode failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetHWProbeCacheForTest()
			ffmpeg := writeFakeFFmpeg(t, tt.probe)
			ok, reason := ffmpegSupportsBackend(transcodeHWQSV, ffmpeg.path, "/dev/dri/renderD128")
			if ok {
				t.Fatal("ffmpegSupportsBackend(qsv) = true, want false")
			}
			if !strings.HasPrefix(reason, tt.want) {
				t.Fatalf("reason = %q, want prefix %q", reason, tt.want)
			}
		})
	}

	resetHWProbeCacheForTest()
	ffmpeg := writeFakeFFmpeg(t, successfulQSVProbe())
	if ok, reason := ffmpegSupportsBackend(transcodeHWQSV, ffmpeg.path, "/dev/dri/renderD128"); !ok {
		t.Fatalf("ffmpegSupportsBackend(qsv) = false, want true (reason=%q)", reason)
	}
	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	for _, want := range []string{
		"vaapi=va:/dev/dri/renderD128,driver=iHD,kernel_driver=i915,vendor_id=0x8086",
		"qsv=qs@va",
		"testsrc2=size=640x360:rate=1",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("QSV smoke command missing %q; log:\n%s", want, logText)
		}
	}
}

func TestFFmpegSupportsVAAPIRequiresEncoderAndSmoke(t *testing.T) {
	setupHWAccelTest(t)

	resetHWProbeCacheForTest()
	missing := writeFakeFFmpeg(t, fakeFFmpegProbe{vaapiHWAccel: true, smokeOK: true})
	if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, missing.path, "/dev/dri/renderD128"); ok {
		t.Fatal("ffmpegSupportsBackend(vaapi) = true, want false")
	} else if reason != "h264_vaapi encoder unavailable" {
		t.Fatalf("reason = %q, want the missing encoder", reason)
	}

	resetHWProbeCacheForTest()
	ffmpeg := writeFakeFFmpeg(t, successfulVAAPIProbe())
	if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpeg.path, "/dev/dri/renderD128"); !ok {
		t.Fatalf("ffmpegSupportsBackend(vaapi) = false, want true (reason=%q)", reason)
	}
	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "vaapi=hw:/dev/dri/renderD128") {
		t.Fatalf("VAAPI smoke command missing its init chain; log:\n%s", logText)
	}
	if strings.Count(logText, "\n") != 2 {
		t.Fatalf("VAAPI probe ran %d commands, want an encoders listing and one smoke encode; log:\n%s",
			strings.Count(logText, "\n"), logText)
	}
}

func TestFFmpegSupportsNVENCCachesByFFmpegPath(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())

	for i := 0; i < 2; i++ {
		if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "nvenc" {
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
	logData, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatal(err)
	}
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

func TestHWProbeNegativeResultExpires(t *testing.T) {
	setupHWAccelTest(t)
	clock := time.Now()
	hwProbeNow = func() time.Time { return clock }

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	markerPath := filepath.Join(dir, "vaapi-ready")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *-encoders*) echo 'h264_vaapi'; exit 0 ;;
  *) test -e %q ;;
esac
`, markerPath)
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write marker-controlled FFmpeg: %v", err)
	}

	if ok, _ := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpegPath, "/dev/dri/renderD128"); ok {
		t.Fatal("initial VAAPI probe unexpectedly succeeded")
	}
	if err := os.WriteFile(markerPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("enable VAAPI smoke probe: %v", err)
	}
	clock = clock.Add(hwProbeNegativeTTL - time.Millisecond)
	if ok, _ := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpegPath, "/dev/dri/renderD128"); ok {
		t.Fatal("negative result was not retained during its TTL")
	}
	clock = clock.Add(2 * time.Millisecond)
	if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpegPath, "/dev/dri/renderD128"); !ok {
		t.Fatalf("expired negative result was not retried: %s", reason)
	}
	// A positive result is kept for the process lifetime, so removing the
	// marker after success must not change the answer.
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if ok, _ := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpegPath, "/dev/dri/renderD128"); !ok {
		t.Fatal("positive probe result expired, want process-lifetime caching")
	}
}

func TestFFmpegSupportsNVENCSmokeProbeUsesSafeFrameDimensions(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())

	if ok, reason := ffmpegSupportsBackend(transcodeHWNVENC, ffmpeg.path, ""); !ok {
		t.Fatalf("ffmpegSupportsBackend(nvenc) = false, want true (reason=%q)", reason)
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

// stripDevicePrefix compares device lists by basename so expectations stay
// readable against the test's temporary /dev/dri stand-in.
func stripDevicePrefix(devices []string) []string {
	names := make([]string, 0, len(devices))
	for _, device := range devices {
		names = append(names, filepath.Base(device))
	}
	return names
}

func setupHWAccelTest(t *testing.T) *hwAccelTestEnv {
	t.Helper()

	oldDRIDir := defaultDRIDir
	oldNVIDIAControlDevice := defaultNVIDIAControlDevice
	oldNVIDIADeviceGlob := defaultNVIDIADeviceGlob
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

// resetHWProbeCacheForTest clears the probe cache between cases. It delegates
// to the exported invalidation so tests exercise the same seam the re-probe
// action uses rather than a second, drifting implementation.
func resetHWProbeCacheForTest() {
	InvalidateHWProbeCache()
}

func successfulVideoToolboxProbe() fakeFFmpegProbe {
	return fakeFFmpegProbe{videotoolbox: true, h264VT: true, hevcVT: true, smokeOK: true}
}

func TestResolveHWAccelWithFFmpegDarwinUsesVideoToolbox(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, successfulVideoToolboxProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "videotoolbox" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want videotoolbox", got)
	}
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

func TestResolveHWAccelWithFFmpegDarwinFallsBackToNoneWhenProbeFails(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{})

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "none" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want none", got)
	}
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

func TestResolveHWAccelWithFFmpegDarwinFallsBackToNoneWhenSmokeEncodeFails(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{videotoolbox: true, h264VT: true, hevcVT: true})

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "none" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want none when smoke encode fails", got)
	}
}

func TestVideoToolboxProbeSmokesBothEncodersInPortableBitrateMode(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, successfulVideoToolboxProbe())

	if got := ResolveHWAccelWithFFmpeg("auto", ffmpeg.path, ""); got != "videotoolbox" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want videotoolbox", got)
	}
	log, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatalf("read probe log: %v", err)
	}
	// FFmpeg restricts -q:v to Apple Silicon. The portable bitrate path keeps
	// H.264 acceleration available on older Intel Macs as well.
	for _, want := range []string{
		"-c:v h264_videotoolbox -b:v 2000k -maxrate 2000k -bufsize 4000k",
		"-vf format=p010le -c:v hevc_videotoolbox -b:v 2000k -maxrate 2000k -bufsize 4000k",
	} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("probe log missing %q:\n%s", want, log)
		}
	}
}

func TestExplicitVideoToolboxBypassesFFmpegProbe(t *testing.T) {
	setupHWAccelTest(t)

	if got := ResolveHWAccelWithFFmpeg("videotoolbox", "/does/not/exist/ffmpeg", ""); got != "videotoolbox" {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want videotoolbox", got)
	}
}

func TestCachedVideoToolboxProbeExecutesConfiguredRelativePath(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, successfulVideoToolboxProbe())
	// Empty PATH: if the probe cleaned "./ffmpeg" to a bare name it would fall
	// back to PATH lookup and fail instead of executing the configured file.
	t.Setenv("PATH", t.TempDir())
	t.Chdir(filepath.Dir(ffmpeg.path))

	result := cachedVideoToolboxProbe("./" + filepath.Base(ffmpeg.path))
	if !result.available {
		t.Fatalf("probe must execute the configured relative path, got reason %q", result.reason)
	}
}

func TestCachedVideoToolboxProbeKeepsRelativeAndPathSpellingsDistinct(t *testing.T) {
	setupHWAccelTest(t)
	capable := writeFakeFFmpeg(t, successfulVideoToolboxProbe())
	incapable := writeFakeFFmpeg(t, fakeFFmpegProbe{})
	// PATH resolves the bare "ffmpeg" name to the incapable binary while the
	// dot-relative spelling names the capable one; a cleaned cache key would
	// collide the two and let one spelling's verdict leak to the other.
	t.Setenv("PATH", filepath.Dir(incapable.path))
	t.Chdir(filepath.Dir(capable.path))

	if result := cachedVideoToolboxProbe("./" + filepath.Base(capable.path)); !result.available {
		t.Fatalf("relative spelling should probe the capable binary, got %q", result.reason)
	}
	if result := cachedVideoToolboxProbe("ffmpeg"); result.available {
		t.Fatal("PATH spelling should probe the incapable binary, not reuse the relative spelling's cache entry")
	}
}

func TestStartupRetryHWAccel(t *testing.T) {
	// Explicit values bypass ffmpeg probing, keeping this host-independent.
	if got := StartupRetryHWAccel(TranscodeOpts{HWAccel: "videotoolbox", FFmpegPath: "/does/not/exist"}); got != "none" {
		t.Fatalf("videotoolbox retry accel = %q, want none (no alternate device to move to)", got)
	}
	for _, accel := range []string{"qsv", "vaapi", "nvenc", "none"} {
		if got := StartupRetryHWAccel(TranscodeOpts{HWAccel: accel, FFmpegPath: "/does/not/exist"}); got != accel {
			t.Fatalf("%s retry accel = %q, want unchanged", accel, got)
		}
	}
	if got := StartupRetryHWAccel(TranscodeOpts{
		HWAccel: "videotoolbox", FFmpegPath: "/does/not/exist", ToneMapMode: tonemap.ModeHardware,
	}); got != "videotoolbox" {
		t.Fatalf("hardware tone-map retry accel = %q, want unchanged frozen executor", got)
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

	log, err := os.ReadFile(ffmpeg.logPath)
	if err != nil {
		t.Fatalf("read probe log: %v", err)
	}
	if got := strings.Count(string(log), "-hwaccels"); got != 1 {
		t.Fatalf("concurrent cold probes ran %d ffmpeg invocations, want 1 (coalesced):\n%s", got, log)
	}
}

func TestCachedVideoToolboxProbeRetriesNegativeResultsAfterExpiry(t *testing.T) {
	setupHWAccelTest(t)
	oldDelay := videoToolboxProbeRetryDelay
	videoToolboxProbeRetryDelay = 0
	t.Cleanup(func() { videoToolboxProbeRetryDelay = oldDelay })

	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{})
	if result := cachedVideoToolboxProbe(ffmpeg.path); result.available {
		t.Fatal("failing fake should probe unavailable")
	}

	// The hardware "recovers": the same binary now answers every probe.
	writeFakeFFmpegScript(t, ffmpeg.path, ffmpeg.logPath, successfulVideoToolboxProbe())
	if result := cachedVideoToolboxProbe(ffmpeg.path); !result.available {
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
	// per-command bound, plus spawn slack.
	wantThree := time.Duration(3*(3+2))*hwProbeCommandTimeout + hwAccelWalkSlack
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

// The walk is capped at the same device ceiling the tone-map matrix is, because
// the budget every caller allows is derived from that ceiling. Probing past it
// would guarantee the capability request is canceled before the walk finishes.
func TestCollectHWCandidatesCapsTheProbedDeviceSet(t *testing.T) {
	env := setupHWAccelTest(t)
	configured := make([]string, 0, tonemap.MaxProbedDevices+3)
	for i := range tonemap.MaxProbedDevices + 3 {
		name := "renderD" + strconv.Itoa(128+i)
		env.addRenderDevice(t, name, "0x8086")
		configured = append(configured, env.devicePath(name))
	}

	candidates := collectHWCandidates(strings.Join(configured, ","))
	if got := len(candidates.probeDevicesFor(transcodeHWQSV)); got != tonemap.MaxProbedDevices {
		t.Fatalf("qsv probe devices = %d, want the %d cap", got, tonemap.MaxProbedDevices)
	}

	// The walk therefore stays inside the budget its callers allow, which is the
	// property the cap exists for.
	if walk, ceiling := hwAccelWalkTimeout(candidates), MaxCapabilityRequestTimeout(); walk >= ceiling {
		t.Fatalf("walk budget %v is not below the %v callers allow", walk, ceiling)
	}

	// Every configured device is still reported, capped or not: the inventory is
	// what an operator reads, and truncating it would hide hardware that exists.
	if got := len(candidates.devicesFor(transcodeHWQSV)); got != tonemap.MaxProbedDevices {
		t.Fatalf("qsv reported devices = %d, want the probed set", got)
	}
	if got := len(candidates.renderDevices); got != len(configured) {
		t.Fatalf("render devices = %d, want all %d enumerated", got, len(configured))
	}

	// A set inside the cap is untouched.
	small := configured[:3]
	if got := len(collectHWCandidates(strings.Join(small, ",")).probeDevicesFor(transcodeHWQSV)); got != 3 {
		t.Fatalf("qsv probe devices = %d, want the 3 configured", got)
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

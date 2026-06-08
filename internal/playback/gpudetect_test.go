package playback

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if probe.hang {
		script += "sleep 5\n"
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
	nvencProbeCache.byPath = make(map[string]nvencProbeResult)
}

package playback

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	defaultDRIDir              = "/dev/dri"
	defaultNVIDIAControlDevice = "/dev/nvidiactl"
	defaultNVIDIADeviceGlob    = "/dev/nvidia[0-9]*"
	sysClassDRMDir             = "/sys/class/drm"
	currentGOOS                = runtime.GOOS
	nvencProbeCommandTimeout   = 3 * time.Second
)

type nvencProbeResult struct {
	available bool
	reason    string
}

var nvencProbeCache = struct {
	sync.Mutex
	byPath map[string]nvencProbeResult
}{
	byPath: make(map[string]nvencProbeResult),
}

// HWAccelInfo describes the detected hardware acceleration capability.
type HWAccelInfo struct {
	Resolved      string   `json:"resolved"`
	RenderDevices []string `json:"render_devices"`
	IntelDetected bool     `json:"intel_detected"`
	Source        string   `json:"source"`
	NodeURL       string   `json:"node_url,omitempty"`
}

// DetectHWAccel probes this host's GPU hardware and returns structured info.
func DetectHWAccel() HWAccelInfo {
	return DetectHWAccelWithFFmpeg("")
}

// DetectHWAccelWithFFmpeg probes this host's GPU hardware and configured FFmpeg.
func DetectHWAccelWithFFmpeg(ffmpegPath string) HWAccelInfo {
	devices := listRenderDevices(defaultDRIDir)
	intel := false
	for _, d := range devices {
		if isIntelDevice(d) {
			intel = true
			break
		}
	}
	return HWAccelInfo{
		Resolved:      ResolveHWAccelWithFFmpeg("auto", ffmpegPath),
		RenderDevices: devices,
		IntelDetected: intel,
		Source:        "local",
	}
}

// PickRenderDevice returns the GPU render device path to use.
// If explicit is non-empty, it is returned as-is.
// Otherwise, it attempts to discover a render device under /dev/dri/.
// Returns empty string if no device is found (caller should fall back to CPU).
func PickRenderDevice(explicit string) string {
	if explicit != "" {
		return explicit
	}
	dev := detectRenderDevice(defaultDRIDir)
	if dev != "" {
		slog.Info("auto-detected GPU render device", "device", dev)
	}
	return dev
}

// ResolveHWAccel resolves "auto" using the default FFmpeg binary.
func ResolveHWAccel(hwAccel string) string {
	return ResolveHWAccelWithFFmpeg(hwAccel, "")
}

// ResolveHWAccelWithFFmpeg resolves "auto" into a concrete acceleration method
// by probing the system and the configured FFmpeg binary.
// Preference order: nvenc > qsv > vaapi > none.
// Non-"auto" values are returned unchanged.
func ResolveHWAccelWithFFmpeg(hwAccel string, ffmpegPath string) string {
	if hwAccel != "auto" {
		return hwAccel
	}
	if currentGOOS != "linux" {
		return "none"
	}

	devices := listRenderDevices(defaultDRIDir)
	var intelDevice string
	var nvidiaDevice string
	var vaapiDevice string
	for _, dev := range devices {
		switch {
		case isNVIDIADevice(dev):
			if nvidiaDevice == "" {
				nvidiaDevice = dev
			}
		case isIntelDevice(dev):
			if intelDevice == "" {
				intelDevice = dev
			}
		default:
			if vaapiDevice == "" {
				vaapiDevice = dev
			}
		}
	}

	if nvidiaDevice != "" || hasNVIDIADevice() {
		if ok, reason := ffmpegSupportsNVENC(ffmpegPath); ok {
			if nvidiaDevice != "" {
				slog.Info("hw_accel=auto: NVIDIA GPU detected, using NVENC", "device", nvidiaDevice)
			} else {
				slog.Info("hw_accel=auto: NVIDIA device detected, using NVENC")
			}
			return "nvenc"
		} else {
			slog.Warn("hw_accel=auto: NVIDIA device detected but FFmpeg NVENC probe failed",
				"ffmpeg", normalizeFFmpegPath(ffmpegPath), "reason", reason)
		}
	}

	if intelDevice != "" {
		slog.Info("hw_accel=auto: Intel GPU detected, using QSV", "device", intelDevice)
		return "qsv"
	}

	if vaapiDevice != "" {
		slog.Info("hw_accel=auto: non-Intel GPU detected, using VAAPI", "device", vaapiDevice)
		return "vaapi"
	}

	slog.Info("hw_accel=auto: no compatible GPU devices found, using software encoding")
	return "none"
}

func ffmpegSupportsNVENC(ffmpegPath string) (bool, string) {
	ffmpegPath = normalizeFFmpegPath(ffmpegPath)
	nvencProbeCache.Lock()
	if result, ok := nvencProbeCache.byPath[ffmpegPath]; ok {
		nvencProbeCache.Unlock()
		return result.available, result.reason
	}
	nvencProbeCache.Unlock()

	result := probeFFmpegNVENC(ffmpegPath)
	nvencProbeCache.Lock()
	nvencProbeCache.byPath[ffmpegPath] = result
	nvencProbeCache.Unlock()
	return result.available, result.reason
}

func normalizeFFmpegPath(ffmpegPath string) string {
	ffmpegPath = strings.TrimSpace(ffmpegPath)
	if ffmpegPath == "" {
		ffmpegPath = ffmpegBinary()
	}
	if strings.ContainsRune(ffmpegPath, os.PathSeparator) {
		return filepath.Clean(ffmpegPath)
	}
	return ffmpegPath
}

func probeFFmpegNVENC(ffmpegPath string) nvencProbeResult {
	if output, err := runFFmpegProbe(ffmpegPath, "-hide_banner", "-hwaccels"); err != nil {
		return nvencProbeResult{reason: "hwaccels probe failed: " + probeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "cuda") {
		return nvencProbeResult{reason: "cuda hwaccel unavailable"}
	}

	if output, err := runFFmpegProbe(ffmpegPath, "-hide_banner", "-encoders"); err != nil {
		return nvencProbeResult{reason: "encoders probe failed: " + probeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "h264_nvenc") {
		return nvencProbeResult{reason: "h264_nvenc encoder unavailable"}
	} else if !ffmpegOutputHasToken(output, "hevc_nvenc") {
		return nvencProbeResult{reason: "hevc_nvenc encoder unavailable"}
	}

	if output, err := runFFmpegProbe(ffmpegPath, "-hide_banner", "-filters"); err != nil {
		return nvencProbeResult{reason: "filters probe failed: " + probeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "scale_cuda") {
		return nvencProbeResult{reason: "scale_cuda filter unavailable"}
	} else if !ffmpegOutputHasToken(output, "hwupload_cuda") {
		return nvencProbeResult{reason: "hwupload_cuda filter unavailable"}
	}

	if output, err := runFFmpegProbe(ffmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-f", "lavfi",
		"-i", "testsrc2=size=64x64:rate=1",
		"-frames:v", "1",
		"-an",
		"-c:v", "h264_nvenc",
		"-f", "null",
		"-",
	); err != nil {
		return nvencProbeResult{reason: "h264_nvenc smoke encode failed: " + probeFailure(err, output)}
	}

	return nvencProbeResult{available: true}
}

func runFFmpegProbe(ffmpegPath string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), nvencProbeCommandTimeout)
	defer cancel()
	return exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
}

func ffmpegOutputHasToken(output []byte, token string) bool {
	for _, field := range strings.Fields(string(output)) {
		if strings.EqualFold(field, token) {
			return true
		}
	}
	return false
}

func probeFailure(err error, output []byte) string {
	message := strings.TrimSpace(err.Error())
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
		if len(trimmed) > 240 {
			trimmed = trimmed[:240] + "..."
		}
		message += ": " + trimmed
	}
	return message
}

// listRenderDevices returns all accessible /dev/dri/renderD* paths, sorted.
func listRenderDevices(driDir string) []string {
	pattern := filepath.Join(driDir, "renderD*")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)

	var accessible []string
	for _, dev := range matches {
		if f, err := os.Open(dev); err == nil {
			f.Close()
			accessible = append(accessible, dev)
		}
	}
	return accessible
}

// isIntelDevice checks whether a render device belongs to an Intel GPU by
// reading the PCI vendor ID from sysfs. Intel vendor ID is 0x8086.
func isIntelDevice(renderDevPath string) bool {
	// /dev/dri/renderD128 → card name "renderD128"
	name := filepath.Base(renderDevPath)
	vendorPath := filepath.Join(sysClassDRMDir, name, "device", "vendor")
	data, err := os.ReadFile(vendorPath)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "0x8086"
}

// isNVIDIADevice checks whether a render device belongs to an NVIDIA GPU by
// reading the PCI vendor ID from sysfs. NVIDIA vendor ID is 0x10de.
func isNVIDIADevice(renderDevPath string) bool {
	name := filepath.Base(renderDevPath)
	vendorPath := filepath.Join(sysClassDRMDir, name, "device", "vendor")
	data, err := os.ReadFile(vendorPath)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "0x10de"
}

func hasNVIDIADevice() bool {
	if file, err := os.Open(defaultNVIDIAControlDevice); err == nil {
		file.Close()
		return true
	}
	matches, err := filepath.Glob(defaultNVIDIADeviceGlob)
	if err != nil || len(matches) == 0 {
		return false
	}
	for _, dev := range matches {
		if file, err := os.Open(dev); err == nil {
			file.Close()
			return true
		}
	}
	return false
}

// detectRenderDevice enumerates /dev/dri/renderD* and returns the first
// available device, or empty string if none found.
func detectRenderDevice(driDir string) string {
	devices := listRenderDevices(driDir)
	if len(devices) > 0 {
		return devices[0]
	}
	return ""
}

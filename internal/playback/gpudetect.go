package playback

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
	"golang.org/x/sync/singleflight"
)

var (
	defaultDRIDir              = "/dev/dri"
	defaultNVIDIAControlDevice = "/dev/nvidiactl"
	defaultNVIDIADeviceGlob    = "/dev/nvidia[0-9]*"
	sysClassDRMDir             = "/sys/class/drm"
	currentGOOS                = runtime.GOOS
	nvencProbeCommandTimeout   = 3 * time.Second
	nvencProbeNegativeTTL      = 15 * time.Second
)

type nvencProbeResult struct {
	available bool
	reason    string
}

type nvencProbeCacheEntry struct {
	result    nvencProbeResult
	expiresAt time.Time
}

var nvencProbeCache = struct {
	sync.Mutex
	byPath map[string]nvencProbeCacheEntry
	group  singleflight.Group
}{
	byPath: make(map[string]nvencProbeCacheEntry),
}

// HWAccelInfo describes the detected hardware acceleration capability.
type HWAccelInfo struct {
	Resolved            string               `json:"resolved"`
	RenderDevices       []string             `json:"render_devices"`
	RenderDeviceDetails []RenderDeviceInfo   `json:"render_device_details"`
	IntelDetected       bool                 `json:"intel_detected"`
	Source              string               `json:"source"`
	NodeURL             string               `json:"node_url,omitempty"`
	Transformations     []TransformationV3   `json:"transformations,omitempty"`
	ToneMapCapabilities tonemap.Capabilities `json:"tone_map_capabilities,omitempty"`
	// ProbeRequestTimeoutMillis is the caller-side budget for this node's
	// effective tone-map probe matrix, including endpoint and transport slack.
	ProbeRequestTimeoutMillis int64 `json:"probe_request_timeout_ms,omitempty"`
}

const (
	probeRequestMinTimeout = 5 * time.Second
	probeRequestMaxTimeout = 5 * time.Minute
)

// NormalizeProbeRequestTimeout bounds a node-advertised probe budget while
// preserving the caller's established fallback for a missing advertisement.
func NormalizeProbeRequestTimeout(millis int64, fallback time.Duration) time.Duration {
	if millis <= 0 {
		return fallback
	}
	if millis < probeRequestMinTimeout.Milliseconds() {
		return probeRequestMinTimeout
	}
	if millis > probeRequestMaxTimeout.Milliseconds() {
		return probeRequestMaxTimeout
	}
	return time.Duration(millis) * time.Millisecond
}

// DetectHWAccel probes this host's GPU hardware and returns structured info.
func DetectHWAccel() HWAccelInfo {
	return DetectHWAccelWithFFmpeg("")
}

// DetectHWAccelWithFFmpeg probes this host's GPU hardware and configured FFmpeg.
func DetectHWAccelWithFFmpeg(ffmpegPath string) HWAccelInfo {
	return DetectHWAccelWithFFmpegContext(context.Background(), ffmpegPath)
}

// DetectHWAccelWithFFmpegContext probes this host without outliving ctx.
func DetectHWAccelWithFFmpegContext(ctx context.Context, ffmpegPath string) HWAccelInfo {
	devices := listRenderDevices(defaultDRIDir)
	intel := false
	for _, d := range devices {
		if isIntelDevice(d) {
			intel = true
			break
		}
	}
	return HWAccelInfo{
		Resolved:            ResolveHWAccelWithFFmpegContext(ctx, "auto", ffmpegPath),
		RenderDevices:       devices,
		RenderDeviceDetails: renderDeviceDetails(devices),
		IntelDetected:       intel,
		Source:              "local",
	}
}

// PickRenderDevice returns the GPU render device path to use.
// If explicit is non-empty, it is returned as-is — multi-device lists are
// resolved to one device by AcquireHWDevice before args are built, so this
// never sees a list on a live path.
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
	return ResolveHWAccelWithFFmpegContext(context.Background(), hwAccel, ffmpegPath)
}

// ResolveHWAccelWithFFmpegContext resolves auto hardware without allowing any
// FFmpeg capability probe to outlive ctx.
func ResolveHWAccelWithFFmpegContext(ctx context.Context, hwAccel string, ffmpegPath string) string {
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
		if ok, reason := ffmpegSupportsNVENCContext(ctx, ffmpegPath); ok {
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
	return ffmpegSupportsNVENCContext(context.Background(), ffmpegPath)
}

func ffmpegSupportsNVENCContext(ctx context.Context, ffmpegPath string) (bool, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	ffmpegPath = normalizeFFmpegPath(ffmpegPath)
	cacheKey := nvencProbeCacheKey(ffmpegPath)
	commandTimeout := nvencProbeCommandTimeout
	negativeTTL := nvencProbeNegativeTTL
	nvencProbeCache.Lock()
	if entry, ok := nvencProbeCache.byPath[cacheKey]; ok && nvencProbeCacheEntryCurrent(entry, time.Now()) {
		nvencProbeCache.Unlock()
		return entry.result.available, entry.result.reason
	}
	nvencProbeCache.Unlock()

	resultCh := nvencProbeCache.group.DoChan(cacheKey, func() (any, error) {
		nvencProbeCache.Lock()
		cached, ok := nvencProbeCache.byPath[cacheKey]
		nvencProbeCache.Unlock()
		if ok && nvencProbeCacheEntryCurrent(cached, time.Now()) {
			return cached.result, nil
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout+time.Second)
		defer cancel()
		result := probeFFmpegNVENCContext(probeCtx, ffmpegPath, commandTimeout)
		entry := nvencProbeCacheEntry{result: result}
		if !result.available {
			entry.expiresAt = time.Now().Add(negativeTTL)
		}
		nvencProbeCache.Lock()
		nvencProbeCache.byPath[cacheKey] = entry
		nvencProbeCache.Unlock()
		return result, nil
	})
	select {
	case <-ctx.Done():
		return false, ctx.Err().Error()
	case shared := <-resultCh:
		if shared.Err != nil {
			return false, shared.Err.Error()
		}
		result, ok := shared.Val.(nvencProbeResult)
		if !ok {
			return false, "invalid shared NVENC probe result"
		}
		return result.available, result.reason
	}
}

func nvencProbeCacheEntryCurrent(entry nvencProbeCacheEntry, now time.Time) bool {
	return entry.result.available || now.Before(entry.expiresAt)
}

// nvencProbeCacheKey invalidates cached capability results when an FFmpeg
// executable is replaced at the same configured path.
func nvencProbeCacheKey(ffmpegPath string) string {
	identityPath := ffmpegPath
	if !strings.ContainsRune(identityPath, os.PathSeparator) {
		if resolved, err := exec.LookPath(identityPath); err == nil {
			identityPath = resolved
		}
	}
	if absolute, err := filepath.Abs(identityPath); err == nil {
		identityPath = absolute
	}
	info, err := os.Stat(identityPath)
	if err != nil {
		return ffmpegPath
	}
	return fmt.Sprintf("%s\x00%d\x00%d", identityPath, info.Size(), info.ModTime().UnixNano())
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

func probeFFmpegNVENCContext(ctx context.Context, ffmpegPath string, commandTimeout time.Duration) nvencProbeResult {
	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-hwaccels"); err != nil {
		return nvencProbeResult{reason: "hwaccels probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "cuda") {
		return nvencProbeResult{reason: "cuda hwaccel unavailable"}
	}

	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-encoders"); err != nil {
		return nvencProbeResult{reason: "encoders probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "h264_nvenc") {
		return nvencProbeResult{reason: "h264_nvenc encoder unavailable"}
	} else if !ffmpegOutputHasToken(output, "hevc_nvenc") {
		return nvencProbeResult{reason: "hevc_nvenc encoder unavailable"}
	}

	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-filters"); err != nil {
		return nvencProbeResult{reason: "filters probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "scale_cuda") {
		return nvencProbeResult{reason: "scale_cuda filter unavailable"}
	} else if !ffmpegOutputHasToken(output, "hwupload_cuda") {
		return nvencProbeResult{reason: "hwupload_cuda filter unavailable"}
	}

	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-f", "lavfi",
		"-i", "testsrc2=size=640x360:rate=1",
		"-frames:v", "1",
		"-an",
		"-c:v", "h264_nvenc",
		"-f", "null",
		"-",
	); err != nil {
		return nvencProbeResult{reason: "h264_nvenc smoke encode failed: " + FormatFFmpegProbeFailure(err, output)}
	}

	return nvencProbeResult{available: true}
}

func runFFmpegProbe(ctx context.Context, timeout time.Duration, ffmpegPath string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

// FormatFFmpegProbeFailure combines a probe error with bounded command output.
func FormatFFmpegProbeFailure(err error, output []byte) string {
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

// RenderDeviceInfo describes one render device for operator-facing surfaces.
type RenderDeviceInfo struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// describeRenderDevice builds a short human label for a render device from
// its sysfs PCI vendor/device ids; best-effort, never fails.
func describeRenderDevice(renderDevPath string) string {
	name := filepath.Base(renderDevPath)
	vendor := readSysfsID(filepath.Join(sysClassDRMDir, name, "device", "vendor"))
	label := ""
	switch vendor {
	case "0x8086":
		label = "Intel GPU"
	case "0x10de":
		label = "NVIDIA GPU"
	case "0x1002":
		label = "AMD GPU"
	case "":
		return "GPU"
	default:
		label = "GPU (vendor " + vendor + ")"
	}
	if device := readSysfsID(filepath.Join(sysClassDRMDir, name, "device", "device")); device != "" && vendor != "0x1002" {
		label += " (" + device + ")"
	}
	return label
}

func readSysfsID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// renderDeviceDetails describes every listed device.
func renderDeviceDetails(devices []string) []RenderDeviceInfo {
	details := make([]RenderDeviceInfo, 0, len(devices))
	for _, device := range devices {
		details = append(details, RenderDeviceInfo{
			Path:        device,
			Description: describeRenderDevice(device),
		})
	}
	return details
}

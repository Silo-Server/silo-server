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

const darwinGOOS = "darwin"

const (
	ffmpegFlagHideBanner = "-hide_banner"
	ffmpegFlagLogLevel   = "-loglevel"
	ffmpegLogLevelError  = "error"
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

type hardwareProbeResult struct {
	available     bool
	reason        string
	h264Available bool
	hevcAvailable bool
}

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

// videoToolboxProbeRetryDelay bounds how long a negative probe result is
// trusted. A transient failure (probe timeout, hardware session contention
// during a burst of playback starts) must not pin auto mode to software for
// the process lifetime; successful fully-capable probes are cached forever.
var videoToolboxProbeRetryDelay = time.Minute

type videoToolboxProbeEntry struct {
	result    hardwareProbeResult
	expiresAt time.Time // zero: cached for the process lifetime
}

type videoToolboxProbeCall struct {
	done   chan struct{}
	result hardwareProbeResult
}

var videoToolboxProbes = struct {
	sync.Mutex
	byPath   map[string]videoToolboxProbeEntry
	inFlight map[string]*videoToolboxProbeCall
}{
	byPath:   make(map[string]videoToolboxProbeEntry),
	inFlight: make(map[string]*videoToolboxProbeCall),
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

// ResolveHWAccelWithFFmpegContext resolves auto hardware without blocking the
// caller past ctx. A coalesced probe may continue for other callers and cache
// its bounded result after this caller leaves.
func ResolveHWAccelWithFFmpegContext(ctx context.Context, hwAccel string, ffmpegPath string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if hwAccel != "auto" {
		return hwAccel
	}
	if currentGOOS == darwinGOOS {
		if ok, reason := ffmpegSupportsVideoToolboxContext(ctx, ffmpegPath); ok {
			slog.InfoContext(ctx, "hw_accel=auto: macOS detected, using VideoToolbox")
			return transcodeHWVideoToolbox
		} else {
			slog.WarnContext(ctx, "hw_accel=auto: macOS detected but FFmpeg VideoToolbox probe failed",
				"ffmpeg", normalizeFFmpegPath(ffmpegPath), "reason", reason)
		}
		return transcodeHWNone
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

func ffmpegSupportsVideoToolboxContext(ctx context.Context, ffmpegPath string) (bool, string) {
	result := cachedVideoToolboxProbeContext(ctx, ffmpegPath)
	return result.available, result.reason
}

func videoToolboxSupportsTargetCodec(ffmpegPath, codec string) (bool, string) {
	return videoToolboxSupportsTargetCodecContext(context.Background(), ffmpegPath, codec)
}

func videoToolboxSupportsTargetCodecContext(ctx context.Context, ffmpegPath, codec string) (bool, string) {
	result := cachedVideoToolboxProbeContext(ctx, ffmpegPath)
	if strings.EqualFold(strings.TrimSpace(codec), transcodeCodecHEVC) {
		if result.hevcAvailable {
			return true, ""
		}
		return false, "hevc_videotoolbox encoder unavailable or failed its smoke encode"
	}
	if result.h264Available {
		return true, ""
	}
	return false, result.reason
}

func cachedVideoToolboxProbe(ffmpegPath string) hardwareProbeResult {
	return cachedVideoToolboxProbeContext(context.Background(), ffmpegPath)
}

func cachedVideoToolboxProbeContext(ctx context.Context, ffmpegPath string) hardwareProbeResult {
	if ctx == nil {
		ctx = context.Background()
	}
	// Execute the exact path the transcode will run and cache on that same
	// spelling: filepath.Clean would turn "./ffmpeg" into a bare name that
	// collides with the PATH-resolved executable, which can be a different
	// binary with different capabilities.
	execPath := probeExecFFmpegPath(ffmpegPath)
	cacheKey := videoToolboxProbeCacheKey(execPath)
	videoToolboxProbes.Lock()
	if entry, ok := videoToolboxProbes.byPath[cacheKey]; ok {
		if entry.expiresAt.IsZero() || time.Now().Before(entry.expiresAt) {
			videoToolboxProbes.Unlock()
			return entry.result
		}
		delete(videoToolboxProbes.byPath, cacheKey)
	}
	// Coalesce concurrent cold-cache probes: a burst of playback starts must
	// not race overlapping smoke encodes (and let a contention failure
	// overwrite a success).
	if call, ok := videoToolboxProbes.inFlight[cacheKey]; ok {
		videoToolboxProbes.Unlock()
		select {
		case <-call.done:
			return call.result
		case <-ctx.Done():
			return hardwareProbeResult{reason: ctx.Err().Error()}
		}
	}
	call := &videoToolboxProbeCall{done: make(chan struct{})}
	videoToolboxProbes.inFlight[cacheKey] = call
	videoToolboxProbes.Unlock()

	commandTimeout := nvencProbeCommandTimeout
	retryDelay := videoToolboxProbeRetryDelay
	go func() {
		probeCtx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout+time.Second)
		defer cancel()
		result := probeFFmpegVideoToolboxContext(probeCtx, execPath, commandTimeout)
		entry := videoToolboxProbeEntry{result: result}
		if !result.available || !result.hevcAvailable {
			entry.expiresAt = time.Now().Add(retryDelay)
		}
		videoToolboxProbes.Lock()
		videoToolboxProbes.byPath[cacheKey] = entry
		call.result = result
		delete(videoToolboxProbes.inFlight, cacheKey)
		close(call.done)
		videoToolboxProbes.Unlock()
	}()

	select {
	case <-call.done:
		return call.result
	case <-ctx.Done():
		return hardwareProbeResult{reason: ctx.Err().Error()}
	}
}

// videoToolboxProbeCacheKey keeps configured spellings distinct while also
// invalidating a cached verdict when that spelling resolves to a replaced
// executable. This matters for Homebrew upgrades that swap a symlink target
// while Silo remains running.
func videoToolboxProbeCacheKey(execPath string) string {
	return execPath + "\x00" + nvencProbeCacheKey(execPath)
}

// StartupRetryHWAccel returns the acceleration for the single retry after a
// transcode dies before producing its first segment. VideoToolbox has no
// alternate render device to move to, so an ordinary encode retries on the
// CPU. A frozen hardware tone-map recipe cannot take that acceleration-only
// shortcut because its mode and filter would no longer describe the executor;
// the owner must replan it as a complete software recipe instead. Every other
// accel keeps its configured value and moves render devices via AvoidHWDevice.
func StartupRetryHWAccel(opts TranscodeOpts) string {
	if opts.ToneMapMode != tonemap.ModeHardware &&
		ResolveHWAccelWithFFmpeg(opts.HWAccel, opts.FFmpegPath) == transcodeHWVideoToolbox {
		return transcodeHWNone
	}
	return opts.HWAccel
}

// probeExecFFmpegPath returns the binary a capability probe must execute for
// the configured path: the configured spelling verbatim (relative paths
// included), or the process-global discovery when unset.
func probeExecFFmpegPath(ffmpegPath string) string {
	ffmpegPath = strings.TrimSpace(ffmpegPath)
	if ffmpegPath == "" {
		return ffmpegBinary()
	}
	return ffmpegPath
}

// probeFFmpegVideoToolbox verifies the configured FFmpeg exposes VideoToolbox
// decode plus H.264 encode, then smoke-encodes each advertised encoder in the
// portable bitrate mode used by the transcode builder. H.264 is the required
// baseline because Silo's current playback ladder targets H.264; HEVC is
// recorded independently so older Intel Macs can still accelerate H.264 while
// an HEVC recipe falls back to software. No filter probes: the VideoToolbox
// pipeline keeps decoded frames in system memory, so the regular software
// filter graph applies (see appendHWAccelArgs).
func probeFFmpegVideoToolboxContext(ctx context.Context, ffmpegPath string, commandTimeout time.Duration) hardwareProbeResult {
	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-hwaccels"); err != nil {
		return hardwareProbeResult{reason: "hwaccels probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "videotoolbox") {
		return hardwareProbeResult{reason: "videotoolbox hwaccel unavailable"}
	}

	output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-encoders")
	if err != nil {
		return hardwareProbeResult{reason: "encoders probe failed: " + FormatFFmpegProbeFailure(err, output)}
	}
	if !ffmpegOutputHasToken(output, "h264_videotoolbox") {
		return hardwareProbeResult{reason: "h264_videotoolbox encoder unavailable"}
	}

	smoke := func(encoder string, extraArgs ...string) ([]byte, error) {
		args := []string{
			ffmpegFlagHideBanner,
			ffmpegFlagLogLevel, ffmpegLogLevelError,
			"-f", "lavfi",
			"-i", "testsrc2=size=640x360:rate=1",
			"-frames:v", "1",
			"-an",
		}
		args = append(args, extraArgs...)
		args = append(args,
			"-c:v", encoder,
			"-b:v", "2000k",
			"-maxrate", "2000k",
			"-bufsize", "4000k",
			"-f", "null",
			"-",
		)
		return runFFmpegProbe(ctx, commandTimeout, ffmpegPath, args...)
	}

	h264Output, err := smoke("h264_videotoolbox")
	if err != nil {
		return hardwareProbeResult{reason: "h264_videotoolbox smoke encode failed: " + FormatFFmpegProbeFailure(err, h264Output)}
	}

	result := hardwareProbeResult{available: true, h264Available: true}
	if ffmpegOutputHasToken(output, "hevc_videotoolbox") {
		// Probe the 10-bit session the HEVC transcode path actually creates:
		// hevc passes the source bit depth through (p010 for HDR10), so an
		// 8-bit-only encoder must not advertise HEVC availability.
		hevcOutput, hevcErr := smoke("hevc_videotoolbox", "-vf", "format=p010le")
		result.hevcAvailable = hevcErr == nil
		if hevcErr != nil {
			result.reason = "hevc_videotoolbox smoke encode failed: " + FormatFFmpegProbeFailure(hevcErr, hevcOutput)
		}
	}
	return result
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

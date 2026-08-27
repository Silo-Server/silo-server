package tonemap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	probeCommandTimeout = 5 * time.Second
	probeNegativeTTL    = 15 * time.Second
	probeTimeoutSlack   = time.Second
	probeEndpointSlack  = 20 * time.Second
	probeRequestSlack   = 5 * time.Second
)

// One deterministic 64x64 Main 10 HEVC frame. Keeping the compressed fixture
// in the binary lets production probes exercise the real decoder without
// depending on a media mount or generating source files with an encoder whose
// availability is itself under test.
const decodeProbeFixtureBitDepth = 10

const decodeProbeFixtureBase64 = "AAAAAUABDAH//wIgAAADAJAAAAMAAAMAHpWUCQAAAAFCAQECIAAAAwCQAAADAAADAB6gIIEE2WVlSkwvAWgIAAADAAgAAAMACEAAAAABRAHAc8CJAAABKAGsTtcff/U+nK/q+A=="

// CommandRunner executes a bounded external command and returns its combined
// output. Tests inject it to model individual FFmpeg capabilities and failures.
type CommandRunner func(context.Context, string, ...string) ([]byte, error)

// probeCacheEntry stores either a permanent complete capability result or a
// short-lived incomplete result that is eligible for retry.
type probeCacheEntry struct {
	capabilities Capabilities
	expiresAt    time.Time
}

var probeCache = struct {
	sync.Mutex
	entries map[string]probeCacheEntry
	group   singleflight.Group
}{entries: make(map[string]probeCacheEntry)}

// Probe returns the cached, smoke-tested tone-map capabilities for an FFmpeg
// binary and hardware configuration.
func Probe(ctx context.Context, ffmpegPath, hardwareBackend, hardwareDevice string) (Capabilities, error) {
	return probeCached(ctx, ffmpegPath, hardwareBackend, hardwareDevice, runCommand, time.Now)
}

// probeCached coalesces identical probes without allowing one caller's
// cancellation to abort the shared work needed by other playback requests.
func probeCached(ctx context.Context, ffmpegPath, hardwareBackend, hardwareDevice string, run CommandRunner, now func() time.Time) (Capabilities, error) {
	key := probeCacheKey(ffmpegPath, hardwareBackend, hardwareDevice)
	probeCache.Lock()
	if cached, ok := probeCache.entries[key]; ok && probeCacheEntryCurrent(cached, now()) {
		result := append(Capabilities(nil), cached.capabilities...)
		probeCache.Unlock()
		return result, nil
	}
	probeCache.Unlock()

	resultCh := probeCache.group.DoChan(key, func() (any, error) {
		probeCache.Lock()
		cached, ok := probeCache.entries[key]
		probeCache.Unlock()
		if ok && probeCacheEntryCurrent(cached, now()) {
			return append(Capabilities(nil), cached.capabilities...), nil
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), ProbeTotalTimeout(hardwareBackend, hardwareDevice))
		defer cancel()
		result, err := probeWithRunner(probeCtx, ffmpegPath, hardwareBackend, hardwareDevice, run)
		if err != nil {
			return nil, err
		}
		entry := probeCacheEntry{capabilities: append(Capabilities(nil), result...)}
		if !probeCapabilitiesComplete(result, hardwareBackend) {
			entry.expiresAt = now().Add(probeNegativeTTL)
		}
		probeCache.Lock()
		probeCache.entries[key] = entry
		probeCache.Unlock()
		return result, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		capabilities, ok := result.Val.(Capabilities)
		if !ok {
			return nil, errors.New("invalid shared tone-map probe result")
		}
		return append(Capabilities(nil), capabilities...), nil
	}
}

// probeWithRunner preserves deadline and cancellation failures from any
// bounded FFmpeg command. Ordinary FFmpeg failures still mean the executor is
// genuinely unsupported and produce a completed empty inventory.
func probeWithRunner(
	ctx context.Context,
	ffmpegPath, hardwareBackend, hardwareDevice string,
	run CommandRunner,
) (Capabilities, error) {
	var transientErr error
	trackingRunner := func(commandCtx context.Context, name string, args ...string) ([]byte, error) {
		output, err := run(commandCtx, name, args...)
		if commandErr := commandCtx.Err(); commandErr != nil {
			transientErr = errors.Join(transientErr, commandErr)
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			transientErr = errors.Join(transientErr, err)
		}
		return output, err
	}
	capabilities := ProbeWithRunner(ctx, ffmpegPath, hardwareBackend, hardwareDevice, trackingRunner)
	transientErr = errors.Join(transientErr, ctx.Err())
	if transientErr != nil {
		return nil, transientErr
	}
	return capabilities, nil
}

// probeCacheKey binds reusable capabilities to the resolved FFmpeg binary and
// the driver facts for every configured hardware device.
func probeCacheKey(ffmpegPath, hardwareBackend, hardwareDevice string) string {
	binaryIdentity := strings.TrimSpace(ffmpegPath)
	if _, cacheKey, cacheable := ffmpegBinaryCacheKey(binaryIdentity); cacheable {
		binaryIdentity = cacheKey
	}
	backend := strings.ToLower(strings.TrimSpace(hardwareBackend))
	device := strings.TrimSpace(hardwareDevice)
	driverIdentities := make([]string, 0)
	switch backend {
	case BackendQSV, BackendVAAPI, BackendNVENC, BackendVideoToolbox:
		devices := probeDevices(device, backend)
		driverIdentities = make([]string, 0, len(devices))
		for _, configuredDevice := range devices {
			driverIdentities = append(driverIdentities, driverFingerprint(backend, configuredDevice))
		}
	}
	return strings.Join([]string{binaryIdentity, backend, device, strings.Join(driverIdentities, ",")}, "\x00")
}

// probeCacheEntryCurrent reports whether a complete result or unexpired
// incomplete result may be reused.
func probeCacheEntryCurrent(entry probeCacheEntry, now time.Time) bool {
	return entry.expiresAt.IsZero() || now.Before(entry.expiresAt)
}

// probeCapabilitiesComplete reports whether discovery found a reusable result
// for every executor class it was asked to inspect. A software capability does
// not make a missing configured hardware executor permanent: temporary device
// contention must be retried after the negative-cache interval.
func probeCapabilitiesComplete(capabilities Capabilities, hardwareBackend string) bool {
	if !capabilityCoversAllSourceKinds(capabilities, ModeSoftware, BackendSoftware) {
		return false
	}
	backend := strings.ToLower(strings.TrimSpace(hardwareBackend))
	switch backend {
	case BackendQSV, BackendVAAPI, BackendNVENC, BackendVideoToolbox:
		return capabilityCoversAllSourceKinds(capabilities, ModeHardware, backend)
	default:
		return true
	}
}

func capabilityCoversAllSourceKinds(capabilities Capabilities, mode Mode, backend string) bool {
	index := slices.IndexFunc(capabilities, func(capability Capability) bool {
		return capability.Mode == mode && capability.Backend == backend
	})
	if index < 0 {
		return false
	}
	return !slices.ContainsFunc(AllSourceKinds(), func(kind SourceKind) bool {
		return !slices.Contains(capabilities[index].SourceKinds, kind)
	})
}

// ProbeTotalTimeout budgets one bounded deadline for every listing and smoke
// command the selected backend and device set can execute.
func ProbeTotalTimeout(hardwareBackend, hardwareDevice string) time.Duration {
	commandCount := 2 + len(AllSourceKinds())
	backend := strings.ToLower(strings.TrimSpace(hardwareBackend))
	switch backend {
	case BackendQSV, BackendVAAPI, BackendNVENC, BackendVideoToolbox:
		commandCount += len(AllSourceKinds()) * len(probeDevices(hardwareDevice, backend))
	}
	return time.Duration(commandCount)*probeCommandTimeout + probeTimeoutSlack
}

// ProbeEndpointTimeout includes auto-backend discovery and response overhead
// around the full tone-map command matrix. Callers of a remote capability
// endpoint must allow this budget or a cold, valid node will be abandoned while
// its shared probe is still warming the cache.
func ProbeEndpointTimeout(hardwareBackend, hardwareDevice string) time.Duration {
	backend := strings.ToLower(strings.TrimSpace(hardwareBackend))
	if backend == "" || backend == "auto" {
		backend = BackendQSV
	}
	return ProbeTotalTimeout(backend, hardwareDevice) + probeEndpointSlack
}

// ProbeRequestTimeout gives a remote caller additional transport and response
// margin beyond the server-side endpoint budget.
func ProbeRequestTimeout(hardwareBackend, hardwareDevice string) time.Duration {
	return ProbeEndpointTimeout(hardwareBackend, hardwareDevice) + probeRequestSlack
}

// ProbeWithRunner inventories executors by checking FFmpeg listings and then
// converting a deterministic HEVC frame for every supported source kind.
func ProbeWithRunner(
	ctx context.Context,
	ffmpegPath, hardwareBackend, hardwareDevice string,
	run CommandRunner,
) Capabilities {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	filters, filterErr := runBounded(ctx, run, ffmpegPath, ffmpegHideBannerArg, "-filters")
	encoders, encoderErr := runBounded(ctx, run, ffmpegPath, ffmpegHideBannerArg, "-encoders")
	if filterErr != nil || encoderErr != nil {
		return Capabilities{}
	}
	fixturePath, cleanupFixture, fixtureErr := writeDecodeProbeFixture()
	if fixtureErr != nil {
		return Capabilities{}
	}
	defer cleanupFixture()

	capabilities := make(Capabilities, 0, 2)
	softwareFilter := ""
	if selected, _ := SelectSoftwareFilter(filters); hasToken(filters, "sidedata") {
		softwareFilter = selected
	}
	if softwareFilter != "" && hasToken(encoders, "libx264") {
		kinds := smokeSourceKinds(ctx, run, ffmpegPath, func(kind SourceKind) []string {
			return softwareSmokeArgs(fixturePath, kind, softwareFilter)
		})
		if len(kinds) > 0 {
			capabilities = append(capabilities, Capability{Mode: ModeSoftware, Backend: BackendSoftware, Filter: softwareFilter, SourceKinds: kinds})
		}
	}

	backend := strings.ToLower(strings.TrimSpace(hardwareBackend))
	if hardwareProbeAvailable(backend, filters, encoders) {
		kinds := hardwareSmokeSourceKinds(ctx, run, ffmpegPath, fixturePath, backend, hardwareDevice)
		if len(kinds) > 0 {
			capabilities = append(capabilities, Capability{Mode: ModeHardware, Backend: backend, Filter: hardwareFilter(backend), SourceKinds: kinds})
		}
	}
	return capabilities
}

// hardwareSmokeSourceKinds returns only source kinds converted successfully on
// every configured device so the advertised union is safe for later selection.
func hardwareSmokeSourceKinds(ctx context.Context, run CommandRunner, ffmpegPath, fixturePath, backend, hardwareDevice string) []SourceKind {
	devices := probeDevices(hardwareDevice, backend)
	validated := AllSourceKinds()
	for _, device := range devices {
		supported := smokeSourceKinds(ctx, run, ffmpegPath, func(kind SourceKind) []string {
			return hardwareSmokeArgs(fixturePath, backend, device, kind)
		})
		validated = intersectSourceKinds(validated, supported)
		if len(validated) == 0 {
			break
		}
	}
	return validated
}

// probeDevices parses the configured device list and supplies the backend's
// deterministic default when the setting is empty.
func probeDevices(value, backend string) []string {
	parts := strings.Split(value, ",")
	devices := make([]string, 0, len(parts))
	for _, part := range parts {
		if device := strings.TrimSpace(part); device != "" {
			devices = append(devices, device)
		}
	}
	if len(devices) == 0 {
		if backend == BackendNVENC {
			return []string{"0"}
		}
		if backend == BackendVideoToolbox {
			return []string{""}
		}
		return []string{defaultDRIRenderDevice}
	}
	return devices
}

// intersectSourceKinds preserves the left-hand ordering while retaining source
// kinds supported by both sets.
func intersectSourceKinds(left, right []SourceKind) []SourceKind {
	result := make([]SourceKind, 0, len(left))
	for _, kind := range left {
		for _, candidate := range right {
			if candidate == kind {
				result = append(result, kind)
				break
			}
		}
	}
	return result
}

// hardwareFilter returns the FFmpeg tone-map filter required by a backend.
func hardwareFilter(backend string) string {
	switch backend {
	case BackendQSV:
		return HardwareFilterOpenCL
	case BackendNVENC:
		return HardwareFilterCUDA
	case BackendVideoToolbox:
		return HardwareFilterVideoToolbox
	default:
		return HardwareFilterVAAPI
	}
}

// runCommand executes a probe command and retains stderr alongside stdout for
// capability detection and bounded diagnostics.
func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// runBounded applies the per-command probe deadline within the caller's total
// deadline.
func runBounded(ctx context.Context, run CommandRunner, name string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, probeCommandTimeout)
	defer cancel()
	return run(commandCtx, name, args...)
}

// hasToken performs the case-insensitive listing checks used during probing.
func hasToken(output []byte, token string) bool {
	return bytes.Contains(bytes.ToLower(output), []byte(strings.ToLower(token)))
}

// hardwareProbeAvailable performs the cheap listing gate before device smoke
// tests are attempted for a configured backend.
func hardwareProbeAvailable(backend string, filters, encoders []byte) bool {
	switch backend {
	case BackendQSV:
		return hasToken(filters, HardwareFilterOpenCL) && hasToken(filters, "hwmap") && hasToken(filters, "scale_vaapi") && hasToken(encoders, "h264_qsv")
	case BackendVAAPI:
		return hasToken(filters, HardwareFilterVAAPI) && hasToken(filters, "scale_vaapi") && hasToken(encoders, "h264_vaapi")
	case BackendNVENC:
		return hasToken(filters, HardwareFilterCUDA) && hasToken(filters, "scale_cuda") && hasToken(encoders, "h264_nvenc")
	case BackendVideoToolbox:
		return hasToken(filters, HardwareFilterVideoToolbox) && hasToken(filters, "hwdownload") && hasToken(filters, "sidedata") && hasToken(encoders, "h264_videotoolbox")
	default:
		return false
	}
}

// smokeSourceKinds runs one bounded conversion per source kind and returns only
// the kinds whose command completed successfully.
func smokeSourceKinds(
	ctx context.Context,
	run CommandRunner,
	ffmpegPath string,
	argsFor func(SourceKind) []string,
) []SourceKind {
	kinds := make([]SourceKind, 0, len(AllSourceKinds()))
	for _, kind := range AllSourceKinds() {
		if _, err := runBounded(ctx, run, ffmpegPath, argsFor(kind)...); err == nil {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// softwareSmokeArgs builds a single-frame software conversion command for the
// embedded decoder fixture and selected filter.
func softwareSmokeArgs(fixturePath string, kind SourceKind, filterName string) []string {
	return []string{
		ffmpegHideBannerArg, ffmpegLogLevelArg, ffmpegErrorLogLevel,
		"-f", codecHEVC, "-i", fixturePath,
		"-vf", SoftwareFilter(kind, filterName),
		"-frames:v", "1", "-c:v", "libx264", "-f", "null", "-",
	}
}

// hardwareSmokeArgs builds a single-frame decode, conversion, and encode
// command for one hardware backend, device, and source kind.
func hardwareSmokeArgs(fixturePath, backend, hardwareDevice string, kind SourceKind) []string {
	device := firstDevice(hardwareDevice)
	if device == "" && backend != BackendNVENC && backend != BackendVideoToolbox {
		device = defaultDRIRenderDevice
	}
	base := []string{ffmpegHideBannerArg, ffmpegLogLevelArg, ffmpegErrorLogLevel}
	switch backend {
	case BackendQSV:
		base = append(base,
			"-init_hw_device", qsvVAAPIInitDevice(device),
			"-init_hw_device", "qsv=qs@va",
			"-init_hw_device", "opencl=ocl@va",
			"-filter_hw_device", "va",
			"-hwaccel", BackendVAAPI, "-hwaccel_output_format", BackendVAAPI,
		)
	case BackendVAAPI:
		base = append(base, "-init_hw_device", "vaapi=va:"+device, "-filter_hw_device", "va", "-hwaccel", BackendVAAPI, "-hwaccel_output_format", BackendVAAPI)
	case BackendNVENC:
		cudaDevice := device
		if cudaDevice == "" {
			cudaDevice = "0"
		}
		base = append(base, "-init_hw_device", "cuda=cu:"+cudaDevice, "-filter_hw_device", "cu", "-hwaccel", "cuda", "-hwaccel_output_format", "cuda")
	case BackendVideoToolbox:
		base = append(base, "-hwaccel", BackendVideoToolbox, "-hwaccel_output_format", "videotoolbox_vld")
	}
	base = append(base,
		"-f", codecHEVC, "-i", fixturePath,
		"-vf", hardwareSmokeFilter(backend, kind, decodeProbeFixtureBitDepth),
		"-frames:v", "1", "-c:v", hardwareEncoder(backend), "-f", "null", "-",
	)
	return base
}

// hardwareSmokeFilter builds the backend-specific graph used to validate both
// HDR and already-SDR Dolby Vision base layers.
func hardwareSmokeFilter(backend string, kind SourceKind, sourceVideoBitDepth int) string {
	if backend == BackendVideoToolbox {
		return SourceParameters(kind) + "," + VideoToolboxFilter("iw", "ih") + "," + VideoToolboxDownloadFilter(sourceVideoBitDepth) + "," + HDRMetadataRemovalFilter()
	}
	if backend == BackendNVENC {
		if IsSDRSource(kind) {
			return "hwdownload,format=" + NVENCSoftwareFallbackPixelFormat(sourceVideoBitDepth) + "," + SoftwareFilter(kind, "") + ",format=nv12,hwupload_cuda"
		}
		return SourceParameters(kind) + "," + CUDAFilter() + "," + HDRMetadataRemovalFilter()
	}
	filter := VAAPIFilter(kind)
	if backend == BackendQSV {
		filter = QSVFilter(kind) + "," + QSVInteropFilter()
	}
	return filter + "," + HDRMetadataRemovalFilter()
}

// writeDecodeProbeFixture materializes the embedded HEVC frame and returns an
// idempotent cleanup function for the caller.
func writeDecodeProbeFixture() (string, func(), error) {
	data, err := base64.StdEncoding.DecodeString(decodeProbeFixtureBase64)
	if err != nil {
		return "", func() {}, err
	}
	file, err := os.CreateTemp("", "silo-tonemap-probe-*.hevc")
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

// hardwareEncoder returns the H.264 encoder paired with a probed backend.
func hardwareEncoder(backend string) string {
	switch backend {
	case BackendQSV:
		return "h264_qsv"
	case BackendVAAPI:
		return "h264_vaapi"
	case BackendVideoToolbox:
		return "h264_videotoolbox"
	default:
		return "h264_nvenc"
	}
}

// firstDevice extracts the first configured device for a single FFmpeg command.
func firstDevice(value string) string {
	if i := strings.IndexByte(value, ','); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

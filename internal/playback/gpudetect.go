package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"golang.org/x/sync/singleflight"
)

// GOOS names this package and its tests compare runtime.GOOS against.
const (
	darwinGOOS  = "darwin"
	linuxGOOS   = "linux"
	windowsGOOS = "windows"
)

const (
	ffmpegFlagHideBanner = "-hide_banner"
	ffmpegFlagLogLevel   = "-loglevel"
	ffmpegLogLevelError  = "error"
	// smokeEncodeSource is the synthetic one-frame input every hardware smoke
	// encode reads. Small and deterministic, so a probe costs a frame rather
	// than a file the host may not have.
	smokeEncodeSource = "testsrc2=size=640x360:rate=1"
)

var (
	defaultDRIDir              = "/dev/dri"
	defaultNVIDIAControlDevice = "/dev/nvidiactl"
	defaultNVIDIADeviceGlob    = "/dev/nvidia[0-9]*"
	sysClassDRMDir             = "/sys/class/drm"
	procBootIDPath             = "/proc/sys/kernel/random/boot_id"
	currentGOOS                = runtime.GOOS
	hwProbeCommandTimeout      = 3 * time.Second
	hwProbeNegativeTTL         = 15 * time.Second
	// hwProbeNow is the cache clock; tests advance it instead of sleeping.
	hwProbeNow = time.Now
	// hwProbeFlightStarted, when set, is called on the shared probe goroutine
	// once it has committed to running a probe. It is the seam a test uses to
	// order an invalidation against a flight that is genuinely in progress,
	// rather than sleeping and hoping. Production leaves it nil.
	hwProbeFlightStarted func()
)

// hardwareProbeResult records a VideoToolbox probe, which reports per-codec
// availability because macOS can offer H.264 without HEVC on older hardware.
type hardwareProbeResult struct {
	available     bool
	reason        string
	h264Available bool
	hevcAvailable bool
}

// hwProbeResult records whether one backend was verified end to end on this
// host. reason is populated only for a failure and is operator-facing.
//
// It covers every backend the Linux walk probes, not just NVENC, which is why
// it is not named for one of them.
type hwProbeResult struct {
	available bool
	reason    string
}

type hwProbeCacheEntry struct {
	result    hwProbeResult
	expiresAt time.Time
}

var hwProbeCache = struct {
	sync.Mutex
	entries map[string]hwProbeCacheEntry
	group   singleflight.Group
	// generation counts invalidations. It is part of every cache and
	// singleflight key, which is what makes InvalidateHWProbeCache supersede a
	// probe already in flight rather than merely clearing the map in front of
	// it: the flight stores its result under the generation it started in, and
	// a caller arriving afterwards asks a different key and therefore starts a
	// fresh probe instead of joining the stale one.
	generation uint64
	// verifiedDevices records, per generation and backend, every candidate
	// device whose smoke encode passed, in probe order. Execution reads it so a
	// backend verified on one render node is not then run on another, and so
	// balancing across a configured multi-device list cannot land a workload on
	// a card no probe ever passed; see VerifiedHWDevice and VerifiedHWDevices.
	verifiedDevices map[string][]string
}{
	entries:         make(map[string]hwProbeCacheEntry),
	verifiedDevices: make(map[string][]string),
}

// DetectedBackend reports one hardware backend that has candidate devices on
// this host, together with the outcome of its FFmpeg verification probe.
type DetectedBackend struct {
	Backend string `json:"backend"`
	// Verified reports whether at least one candidate device passed its probe.
	Verified bool `json:"verified"`
	// Devices lists every candidate considered for this backend, in probe order.
	Devices []string `json:"devices,omitempty"`
	// Device is the candidate whose probe passed. NVENC addresses its GPU
	// through the CUDA runtime, so it stays empty there even when verified.
	Device string `json:"device,omitempty"`
	// Reason explains a failure, attributed per device when several were tried.
	Reason string `json:"reason,omitempty"`
	// Skipped reports that no probe was attempted because none of the
	// backend's candidate devices is accessible to this process — a proxy
	// node reading a cluster-wide hw_device meant for the transcode nodes,
	// not a driver failure. Reason still says which devices were skipped.
	Skipped bool `json:"skipped,omitempty"`
}

// videoToolboxProbeRetryDelay bounds how long a negative probe result is
// trusted. A transient failure (probe timeout, hardware session contention
// during a burst of playback starts) must not pin auto mode to software for
// the process lifetime; successful fully-capable probes are cached forever.
var videoToolboxProbeRetryDelay = time.Minute

// videoToolboxProbeStarted is a test seam: it fires inside a VideoToolbox probe
// flight so a test can order an invalidation against it by channel receipt
// rather than by sleeping. Production leaves it nil.
var videoToolboxProbeStarted func()

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
	DetectedBackends    []DetectedBackend    `json:"detected_backends,omitempty"`
	Source              string               `json:"source"`
	NodeURL             string               `json:"node_url,omitempty"`
	Transformations     []TransformationV3   `json:"transformations,omitempty"`
	ToneMapCapabilities tonemap.Capabilities `json:"tone_map_capabilities,omitempty"`
	// BootID is this host's kernel boot identity (Linux only). Paired with a
	// render device's PCI address it distinguishes "same GPU, same boot" from
	// "same device path on a host that rebooted or was replaced".
	BootID string `json:"boot_id,omitempty"`
	// NVIDIAGPUUUIDs lists every GPU nvidia-smi reports on this host, sorted.
	//
	// It exists because a card is not always reachable through a DRM render
	// node. An NVIDIA container is routinely given /dev/nvidia* and the toolkit
	// with no /dev/dri at all: NVENC works, RenderDeviceDetails is empty, and
	// the whole host would otherwise contribute no hardware identity. Two such
	// containers sharing one card would then look like two independent GPUs to
	// the planner — which is precisely the deployment where GPU sharing is most
	// common, and the placement mistake most expensive.
	NVIDIAGPUUUIDs []string `json:"nvidia_gpu_uuids,omitempty"`
	// CapabilityHash summarizes every hardware-identity and capability field
	// below, so a reader can detect change without diffing the whole report.
	// Set by the node that serves the report; see ComputeCapabilityHash.
	CapabilityHash string `json:"capability_hash,omitempty"`
	// ProbeRequestTimeoutMillis is the caller-side budget for this node's
	// effective tone-map probe matrix, including endpoint and transport slack.
	ProbeRequestTimeoutMillis int64 `json:"probe_request_timeout_ms,omitempty"`
}

const probeRequestMinTimeout = 5 * time.Second

// NormalizeProbeRequestTimeout bounds a node-advertised probe budget while
// preserving the caller's established fallback for a missing advertisement.
//
// The ceiling exists because the value comes off the wire from a worker, and a
// caller holds a connection open for it. It is derived from the probe formula
// rather than picked, because a round number picked once was already below what
// a nine-device node legitimately asks for: the API then canceled that node's
// re-probe before its own deadline, every time, and its inventory never landed.
// A ceiling that binds a real configuration is indistinguishable from a bug.
func NormalizeProbeRequestTimeout(millis int64, fallback time.Duration) time.Duration {
	if millis <= 0 {
		return fallback
	}
	if millis < probeRequestMinTimeout.Milliseconds() {
		return probeRequestMinTimeout
	}
	if ceiling := MaxCapabilityRequestTimeout(); millis > ceiling.Milliseconds() {
		return ceiling
	}
	return time.Duration(millis) * time.Millisecond
}

// DetectHWAccel probes this host's GPU hardware and returns structured info.
func DetectHWAccel() HWAccelInfo {
	return DetectHWAccelWithFFmpeg(hwAccelAuto, "", "")
}

// DetectHWAccelWithFFmpeg probes this host's GPU hardware and configured FFmpeg.
func DetectHWAccelWithFFmpeg(hwAccel, ffmpegPath, hwDevice string) HWAccelInfo {
	return DetectHWAccelWithFFmpegContext(context.Background(), hwAccel, ffmpegPath, hwDevice)
}

// DetectHWAccelWithFFmpegContext probes this host without outliving ctx. Unlike
// resolution it verifies every backend with candidate hardware, so an operator
// sees why a present GPU was not selected. Resolved still honors the
// pass-through contract: an explicitly configured backend wins even when its
// probe failed, and the report carries the failure reason.
func DetectHWAccelWithFFmpegContext(ctx context.Context, hwAccel, ffmpegPath, hwDevice string) HWAccelInfo {
	info, _ := DetectHWAccelWithFFmpegContextResult(ctx, hwAccel, ffmpegPath, hwDevice)
	return info
}

// ErrHardwareDetectionIncomplete reports that a detection walk ended before it
// had probed every candidate backend, because its own budget or the caller's
// context ran out.
//
// The report it accompanies is still returned — an operator-facing surface can
// show what was learned — but it must never be hashed and published as this
// host's capabilities. A cut-short walk marks unprobed backends Verified=false
// and resolves to software, which is byte-for-byte what a real hardware failure
// looks like: the API's health sweep would then persist a capability_drift note
// for hardware that is fine, latch it until a clean report arrives, and route
// the node to software encoding in the meantime.
var ErrHardwareDetectionIncomplete = errors.New("hardware detection did not complete within its budget")

// DetectHWAccelWithFFmpegContextResult is DetectHWAccelWithFFmpegContext with
// the walk's completeness reported. Callers that publish or hash the report —
// the node capability endpoints and their background snapshots — must use this
// form and refuse to publish on ErrHardwareDetectionIncomplete.
func DetectHWAccelWithFFmpegContextResult(ctx context.Context, hwAccel, ffmpegPath, hwDevice string) (HWAccelInfo, error) {
	// One listing per walk: the identities below are re-read here, while the
	// probe verdicts they accompany stay cached. That asymmetry is deliberate —
	// a probe is several ffmpeg execs and a listing is one cheap query, and it
	// is the listing that answers "is this card still here".
	resetNVIDIAGPUUUIDs()
	candidates := collectHWCandidates(hwDevice)
	resolved := HWAccelNone
	var detected []DetectedBackend
	complete := true
	switch currentGOOS {
	case linuxGOOS:
		resolved, detected, complete = walkHWAccelBackends(ctx, ffmpegPath, candidates, false)
	case darwinGOOS:
		// macOS has no render devices to walk, but it does have hardware: the
		// same VideoToolbox probe resolution uses. Without this a capable Mac
		// publishes resolved:"none" with no detected backends, and the API
		// stores that as its durable inventory — a software-only node, planned
		// for software tone mapping, with an operator re-probe that cannot
		// verify otherwise because there is nothing here to verify.
		entry := DetectedBackend{Backend: transcodeHWVideoToolbox}
		if ok, reason := ffmpegSupportsVideoToolboxContext(ctx, ffmpegPath); ok {
			entry.Verified = true
			resolved = transcodeHWVideoToolbox
		} else {
			entry.Reason = reason
			// A probe cut short by the caller's deadline is not a verdict about
			// the hardware, and must not be hashed as one.
			complete = ctx.Err() == nil
		}
		detected = append(detected, entry)
	}
	if configured := strings.TrimSpace(hwAccel); configured != "" && configured != hwAccelAuto {
		resolved = configured
	}
	info := HWAccelInfo{
		Resolved:            resolved,
		RenderDevices:       candidates.renderDevices,
		RenderDeviceDetails: renderDeviceDetails(candidates.renderDevices),
		IntelDetected:       candidates.intelPresent,
		DetectedBackends:    detected,
		BootID:              detectBootID(),
		NVIDIAGPUUUIDs:      nvidiaGPUUUIDList(),
		Source:              "local",
	}
	if !complete {
		return info, ErrHardwareDetectionIncomplete
	}
	return info, nil
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

// ResolveHWAccelWithFFmpeg resolves "auto" into a concrete acceleration method
// by probing the system and the configured FFmpeg binary.
// Preference order: nvenc > qsv > vaapi > none.
// Non-"auto" values are returned unchanged.
// hwDevice is the configured playback.hw_device value; probes run against it so
// verification covers the device a transcode will actually open.
func ResolveHWAccelWithFFmpeg(hwAccel, ffmpegPath, hwDevice string) string {
	return ResolveHWAccelWithFFmpegContext(context.Background(), hwAccel, ffmpegPath, hwDevice)
}

// ResolveHWAccelWithFFmpegContext resolves auto hardware without allowing any
// FFmpeg capability probe to outlive ctx. A coalesced probe may continue for
// other callers and cache its bounded result after this caller leaves.
func ResolveHWAccelWithFFmpegContext(ctx context.Context, hwAccel, ffmpegPath, hwDevice string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if hwAccel != hwAccelAuto {
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
	if currentGOOS != linuxGOOS {
		return HWAccelNone
	}
	resolved, _, _ := walkHWAccelBackends(ctx, ffmpegPath, collectHWCandidates(hwDevice), true)
	return resolved
}

// hwAccelAuto is the configured hw_accel value that asks this package to pick
// a backend by probing the host, rather than the operator naming one outright.
const hwAccelAuto = "auto"

// hwAccelPreferenceOrder is the auto-resolution order; the first backend whose
// probe passes wins.
var hwAccelPreferenceOrder = []string{transcodeHWNVENC, transcodeHWQSV, transcodeHWVAAPI}

// hwAccelWalkSlack is the margin the walk deadline carries above the commands
// it may actually run, covering process spawn and the bookkeeping between them.
const hwAccelWalkSlack = 5 * time.Second

// hwAccelWalkTimeout bounds one full backend walk, so a wedged driver cannot
// stretch detection without limit. tonemap.probeEndpointSlack budgets a
// capability request for it.
//
// It is derived from the matrix the walk will actually run rather than fixed,
// because that matrix grows with the device set: every configured render device
// is probed for both QSV and VAAPI, so three Intel devices legitimately need
// more than the thirty seconds a fixed bound allowed. The walk then marked
// itself incomplete while every individual command was still inside its own
// budget, and /hw-capabilities answered 503 for a node that was working — the
// same failure shape as a bound that is a guess rather than a derivation.
func hwAccelWalkTimeout(candidates hwCandidates) time.Duration {
	commands := 0
	for _, backend := range hwAccelPreferenceOrder {
		if !candidates.presentFor(backend) {
			continue
		}
		probe, ok := hwBackendProbeFor(backend)
		if !ok {
			continue
		}
		commands += probe.commandCount * len(candidates.probeDevicesFor(backend))
	}
	if commands == 0 {
		commands = 1
	}
	return time.Duration(commands)*hwProbeCommandTimeout + hwAccelWalkSlack
}

// CapabilityEndpointTimeout is how long one capability endpoint may take to
// answer: the hardware detection walk plus the tone-map matrix and the overhead
// around it.
//
// Both halves scale with the configured device set, and they live in different
// packages — tonemap cannot see the walk, because playback imports tonemap and
// not the reverse. Composing them here is what stops the two from drifting: a
// constant standing in for one of them inside the other has to be raised by
// hand whenever it grows, and the first time the walk grew, it was not.
func CapabilityEndpointTimeout(hwAccel, hwDevice string) time.Duration {
	return hwAccelWalkTimeout(collectHWCandidates(hwDevice)) +
		tonemap.ProbeEndpointTimeout(hwAccel, hwDevice)
}

// RegistryCapabilityEndpointTimeout is how long a capability endpoint that
// probes only the transformation registry may take to answer — a proxy's
// snapshot, which reports what its ffmpeg can do and deliberately walks no
// hardware.
//
// It is composed from the same two halves as CapabilityEndpointTimeout, so the
// two cannot drift apart, but with an empty candidate set rather than the host's
// own: no backend has candidates, so the walk falls to its one-command floor,
// and the tone-map half is asked for the software backend, which budgets no
// per-device matrix.
//
// Host-independence is the point, not an incidental saving. This value is
// advertised on the report and covered by its capability hash, so deriving it
// from /dev/dri would put the host's device count inside a proxy's identity: a
// GPU appearing on a machine that also runs a proxy would move that proxy's
// hash, cost the API a refetch and a planning-cache drop, and announce a change
// to capabilities that are by construction the same.
func RegistryCapabilityEndpointTimeout() time.Duration {
	return hwAccelWalkTimeout(hwCandidates{}) +
		tonemap.ProbeEndpointTimeout(HWAccelNone, "")
}

// RegistryCapabilityRequestTimeout is RegistryCapabilityEndpointTimeout plus the
// transport margin a remote caller needs, mirroring CapabilityRequestTimeout. It
// is what a registry-only node advertises and what a caller of its capability
// endpoint must allow.
func RegistryCapabilityRequestTimeout() time.Duration {
	return RegistryCapabilityEndpointTimeout() + tonemap.ProbeRequestSlack
}

// HWAccelWalkTimeout is how long a hardware detection walk of this host takes at
// worst, for the configured device set.
//
// Exported for a caller that runs one synchronously while holding an HTTP
// connection open and must size its write deadline to cover it: the walk grows
// with the device count and passes the API listener's own write timeout at eight
// Intel render devices, so a response can be lost while every probe is still
// inside its bound. Unlike the ceiling used to clamp what a *remote* node
// advertises, this classifies devices on the host that is about to walk them,
// which is the host asking.
func HWAccelWalkTimeout(hwDevice string) time.Duration {
	return hwAccelWalkTimeout(collectHWCandidates(hwDevice))
}

// CapabilityRequestTimeout is CapabilityEndpointTimeout plus the transport
// margin a remote caller needs. It is what a node advertises and what a caller
// of that node's capability endpoint must allow.
func CapabilityRequestTimeout(hwAccel, hwDevice string) time.Duration {
	return CapabilityEndpointTimeout(hwAccel, hwDevice) + tonemap.ProbeRequestSlack
}

// ColdCapabilityRequestTimeout is how long to allow one capability read of a
// node this process has not read successfully yet.
//
// Cold is when the read is slowest — every probe cache on the node is empty and
// the whole matrix runs — so it is exactly the wrong moment to guess low.
// Getting it low does not slow the read down, it cancels it: the node drops out
// of the capability map mid-matrix and playback plans without it.
//
// Every source is a lower bound on what the read may need, so the answer is the
// largest of them — the caller's fallback included, which is why it is a floor
// and not only a last resort. Overshooting holds a dead node's fetch open a
// little longer; undershooting loses a live one.
//
// Two of those sources describe the node, and neither dominates:
//
//   - What the node advertised in the report stored for it. That is the node's
//     own measurement of its own matrix, it survives an API restart because it
//     is persisted with the report, and it is right even when this replica has
//     never spoken to the node. It is also as old as the report: an operator who
//     has just widened the node's device set has invalidated it.
//   - What the node's effective acceleration policy prices — its own override
//     where it has one, the cluster setting otherwise. This moves the moment an
//     operator edits the node, before any refetch can land, which is exactly
//     when the stored figure is wrong. But it is priced *here*, on an API
//     replica that does not have the node's cards, so device classification
//     falls back to the cheapest backend and it reads as a floor rather than as
//     the truth.
//
// The fallback is what each caller is willing to spend on a node it knows
// nothing about, and a node that has never been inventoried is the one most
// likely to be slow — so a policy that happens to price lower does not lower it.
func ColdCapabilityRequestTimeout(storedReport json.RawMessage, hwAccel, hwDevice string, fallback time.Duration) time.Duration {
	budget := fallback
	if millis := AdvertisedProbeBudgetMillis(storedReport); millis > 0 {
		if advertised := NormalizeProbeRequestTimeout(millis, fallback); advertised > budget {
			budget = advertised
		}
	}
	if priced := CapabilityRequestTimeout(hwAccel, hwDevice); priced > budget {
		budget = priced
	}
	return budget
}

// AdvertisedProbeBudgetMillis reads the probe budget out of a stored capability
// report, or 0 when it names none.
//
// The report is parsed for this one field rather than decoded whole: callers
// that want a budget have no business depending on the shape of an inventory,
// and a report they cannot parse is not a reason to fail — it reads as "no
// budget advertised" and the caller falls back.
func AdvertisedProbeBudgetMillis(storedReport json.RawMessage) int64 {
	if len(storedReport) == 0 {
		return 0
	}
	var advertised struct {
		ProbeRequestTimeoutMillis int64 `json:"probe_request_timeout_ms"`
	}
	if err := json.Unmarshal(storedReport, &advertised); err != nil {
		return 0
	}
	return advertised.ProbeRequestTimeoutMillis
}

// MaxCapabilityRequestTimeout is the largest budget a node may advertise and be
// believed.
//
// It is computed from the command counts rather than by pricing a synthetic
// device list, because classifying devices reads *this* host's sysfs — and the
// host doing the clamping is an API replica that does not have the remote
// node's cards. Fabricated render paths there resolve to no vendor and count as
// VAAPI alone, so the ceiling came out below what a node with a dozen Intel
// devices legitimately advertises, and the clamp then canceled that node
// before its own matrix could finish. A ceiling that depends on where it is
// evaluated is not a ceiling.
func MaxCapabilityRequestTimeout() time.Duration {
	return maxHWAccelWalkTimeout() + tonemap.MaxProbeRequestTimeout()
}

// maxHWAccelWalkTimeout prices the largest walk the cap allows: every device
// classified as Intel, which is the only vendor that draws two backends, plus
// the single NVENC probe that runs regardless of the device list.
func maxHWAccelWalkTimeout() time.Duration {
	perDevice := 0
	for _, backend := range []string{transcodeHWQSV, transcodeHWVAAPI} {
		if probe, ok := hwBackendProbeFor(backend); ok {
			perDevice += probe.commandCount
		}
	}
	commands := perDevice * tonemap.MaxProbedDevices
	if probe, ok := hwBackendProbeFor(transcodeHWNVENC); ok {
		commands += probe.commandCount
	}
	return time.Duration(commands)*hwProbeCommandTimeout + hwAccelWalkSlack
}

// UsableHWDevices truncates a configured device list to the ceiling this host
// will actually use, which is the same ceiling the probe matrix is capped at.
//
// Past it the walk costs more than the budget every caller allows, so probing
// further guarantees the capability request is canceled rather than finished.
// The cap therefore has to bind selection too, not only probing: a device the
// matrix never reached has no verdict behind it, and dispatching a transcode
// there means finding out whether it works after the session has started, on a
// node whose published capabilities say nothing about it. Truncating in one
// place and balancing over the full list in another is only accidentally safe —
// it holds while a walk has recorded verified devices to narrow against, and
// stops holding in a process that has not walked yet.
//
// The devices past the ceiling are still reported in the inventory, so an
// operator can see what was configured, and the omission is logged rather than
// folded silently into a shorter answer.
func UsableHWDevices(devices []string) []string {
	if len(devices) <= tonemap.MaxProbedDevices {
		return devices
	}
	noteHWProbeDevicesTruncated(len(devices))
	return devices[:tonemap.MaxProbedDevices]
}

// hwProbeDevicesTruncatedLogged latches the truncation warning to one line per
// process: a device list is standing configuration, not an event.
var hwProbeDevicesTruncatedLogged sync.Once

func noteHWProbeDevicesTruncated(configured int) {
	hwProbeDevicesTruncatedLogged.Do(func() {
		slog.Warn("hardware detection covers only the first configured devices; the rest are not verified",
			"component", "playback", "configured", configured, "probed", tonemap.MaxProbedDevices)
	})
}

// hwCandidates groups the candidate render devices by the backend each one can
// plausibly drive, before any FFmpeg verification.
type hwCandidates struct {
	// renderDevices is this host's full inventory, reported to operators even
	// when probes are pinned to a configured subset.
	renderDevices []string
	nvidia        []string
	intel         []string
	vaapi         []string
	// accessible records, for a configured probe set only, which devices this
	// process can actually open. nil means the set came from discovery, which
	// already filtered on openability. NVENC never consults it — CUDA names its
	// GPU by index or uuid, neither of which is a file.
	accessible map[string]bool
	// nvencDevice is the CUDA identity a NVENC transcode will actually be given:
	// the first configured hw_device entry, exactly what acquireHWDevice hands
	// execution, or empty for the CUDA default. The probe uses it so a working
	// GPU 0 cannot verify NVENC on behalf of a configured GPU 1 that is absent.
	nvencDevice   string
	nvidiaPresent bool
	// intelPresent describes the inventory rather than the probe set, so a
	// pinned non-Intel device does not hide an Intel GPU from operators.
	intelPresent bool
}

// collectHWCandidates enumerates render devices once and classifies them by
// sysfs vendor id. Probes run against the configured playback.hw_device set
// when there is one, because that — not whatever sorts first under /dev/dri —
// is what a transcode opens. NVIDIA hardware also counts when only the control
// device is exposed, which is how NVENC-only containers appear.
func collectHWCandidates(configuredDevice string) hwCandidates {
	configured := ParseHWDeviceSet(configuredDevice)
	candidates := hwCandidates{
		renderDevices: listRenderDevices(defaultDRIDir),
		// NVENC is never balanced across a list, so the first entry is the one
		// execution uses and therefore the one worth probing.
		nvencDevice: configured.First(),
	}
	probeDevices := configured.List()
	if len(probeDevices) == 0 {
		probeDevices = candidates.renderDevices
	}
	probeDevices = UsableHWDevices(probeDevices)
	if len(configured.List()) > 0 {
		// A configured device this process cannot open can never pass a smoke
		// encode, so it is classified for reporting but never probed. This is
		// the normal state of a proxy node reading the cluster-wide hw_device
		// meant for the transcode nodes.
		candidates.accessible = make(map[string]bool, len(probeDevices))
		for _, device := range probeDevices {
			candidates.accessible[device] = deviceOpenable(device)
		}
	}
	for _, device := range probeDevices {
		switch {
		case isNVIDIADevice(device):
			// NVIDIA render nodes carry no libva driver, so listing one as a
			// VAAPI candidate would only fail a probe another GPU can pass.
			candidates.nvidia = append(candidates.nvidia, device)
		case isIntelDevice(device):
			candidates.intel = append(candidates.intel, device)
			candidates.vaapi = append(candidates.vaapi, device)
		default:
			candidates.vaapi = append(candidates.vaapi, device)
		}
	}
	candidates.nvidiaPresent = len(candidates.nvidia) > 0 || hasNVIDIADevice()
	candidates.intelPresent = len(candidates.intel) > 0
	for _, device := range candidates.renderDevices {
		if candidates.intelPresent {
			break
		}
		candidates.intelPresent = isIntelDevice(device)
	}
	return candidates
}

// devicesFor returns the devices a backend may drive. VAAPI is the generic
// fallback, so every non-NVIDIA candidate belongs to it.
func (c hwCandidates) devicesFor(backend string) []string {
	switch backend {
	case transcodeHWNVENC:
		return c.nvidia
	case transcodeHWQSV:
		return c.intel
	case transcodeHWVAAPI:
		return c.vaapi
	default:
		return nil
	}
}

// presentFor reports whether a backend has hardware worth probing.
func (c hwCandidates) presentFor(backend string) bool {
	if backend == transcodeHWNVENC {
		return c.nvidiaPresent
	}
	return len(c.devicesFor(backend)) > 0
}

// probeDevicesFor returns the devices a backend's smoke encode is tried
// against, in order. NVENC addresses its GPU through the CUDA runtime rather
// than a render node, so it probes once with no device path.
func (c hwCandidates) probeDevicesFor(backend string) []string {
	if backend == transcodeHWNVENC {
		// The configured CUDA identity when there is one, so the smoke encode
		// opens the same GPU -hwaccel_device will name; empty otherwise, which
		// is the CUDA default execution also falls back to.
		return []string{c.nvencDevice}
	}
	return c.devicesFor(backend)
}

// walkHWAccelBackends verifies each backend with candidate hardware in
// preference order and reports the first one whose probe passes. Resolution
// stops there; detection continues so every candidate backend is reported.
//
// complete reports whether every candidate backend was actually probed. It is
// false when the walk budget or the caller's context ran out partway through,
// which leaves unprobed backends indistinguishable from failed ones — see
// ErrHardwareDetectionIncomplete for why a publisher must not hash such a
// report.
func walkHWAccelBackends(ctx context.Context, ffmpegPath string, candidates hwCandidates, stopAtFirstVerified bool) (resolved string, detected []DetectedBackend, complete bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, hwAccelWalkTimeout(candidates))
	defer cancel()
	complete = true
	for _, backend := range hwAccelPreferenceOrder {
		if !candidates.presentFor(backend) {
			continue
		}
		// A caller that has already given up must not start further probes.
		if ctx.Err() != nil {
			complete = false
			break
		}
		entry, probedFully := verifyHWAccelBackend(ctx, backend, ffmpegPath, candidates)
		if !probedFully {
			complete = false
		}
		if !entry.Verified {
			slog.WarnContext(ctx, "hw_accel=auto: candidate hardware failed its FFmpeg probe",
				"backend", backend, "devices", entry.Devices,
				"ffmpeg", normalizeFFmpegPath(ffmpegPath), "reason", entry.Reason)
		} else if resolved == "" {
			resolved = backend
			slog.InfoContext(ctx, "hw_accel=auto: verified hardware backend", "backend", backend, "device", entry.Device)
		}
		detected = append(detected, entry)
		if resolved != "" && stopAtFirstVerified {
			// Stopping early is the caller's instruction, not a budget failure:
			// the remaining backends are lower preference and would not have
			// been selected either way.
			return resolved, detected, complete
		}
	}
	if resolved == "" {
		slog.InfoContext(ctx, "hw_accel=auto: no verified hardware backend, using software encoding")
		return HWAccelNone, detected, complete
	}
	return resolved, detected, complete
}

// verifyHWAccelBackend probes a backend's candidate devices in order. A broken
// GPU sorting ahead of a working one does not disable the backend for the whole
// host: the first device that passes decides the backend's verdict.
//
// Whether the walk continues past that device depends on what the rest of the
// list is for. Discovered candidates are alternatives — execution adopts the one
// device detection verified — so probing the losers costs FFmpeg launches and
// buys nothing. A configured multi-device playback.hw_device is different: it is
// a set the device balancer allocates *across*, so every entry is a device a
// transcode can be handed, and stopping early would leave the inventory
// vouching for cards nothing ever tested while acquireHWDevice happily balanced
// onto them.
//
// complete reports whether every candidate was reached. A device left unprobed
// because the budget ran out is not a device that failed, and the difference is
// invisible in the returned entry.
func verifyHWAccelBackend(ctx context.Context, backend, ffmpegPath string, candidates hwCandidates) (entry DetectedBackend, complete bool) {
	devices := candidates.probeDevicesFor(backend)
	entry = DetectedBackend{Backend: backend, Devices: candidates.devicesFor(backend)}
	reasons := make([]string, 0, len(devices))
	probed := false
	complete = true
	probeEveryDevice := candidates.allocatesAcross(backend)
	// Captured before any probe runs: a verdict earned under this generation is
	// only worth recording if no invalidation has landed by the time it lands.
	generation := hwProbeGeneration()
	for _, device := range devices {
		if ctx.Err() != nil {
			complete = false
			break
		}
		if reason, unprobeable := candidates.unprobeableReason(backend, device); unprobeable {
			reasons = append(reasons, hwProbeFailureReason(len(devices), device, reason))
			continue
		}
		probed = true
		available, reason := ffmpegSupportsBackendContext(ctx, backend, ffmpegPath, device)
		if !available && ctx.Err() != nil {
			// The walk's own budget ran out while this probe was in flight, so
			// the failure describes the deadline and not the hardware. Checking
			// only at the top of the loop misses it entirely on the last
			// candidate of the last backend, and the report would then publish
			// a timeout as a real regression: a new hash, recorded drift, and a
			// node resolved to software with nothing wrong with its GPU.
			complete = false
			break
		}
		if available {
			// Execution has to land on a device a probe passed and not on
			// whatever sorts first under /dev/dri, or a report saying "qsv
			// verified" is paired with a transcode initializing a GPU the probe
			// never touched.
			recordVerifiedHWDevice(generation, backend, device)
			if !entry.Verified {
				entry.Verified = true
				entry.Device = device
			}
			if !probeEveryDevice {
				return entry, complete
			}
			continue
		}
		reasons = append(reasons, hwProbeFailureReason(len(devices), device, reason))
	}
	if entry.Verified {
		// The reasons collected past the first pass belong to devices the
		// balancer will now skip, not to a backend that failed.
		return entry, complete
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "hardware detection budget exhausted before probing "+backend)
	}
	entry.Skipped = !probed && len(devices) > 0 && ctx.Err() == nil
	entry.Reason = strings.Join(reasons, "; ")
	return entry, complete
}

// hwProbeGeneration reads the current invalidation generation.
func hwProbeGeneration() uint64 {
	hwProbeCache.Lock()
	defer hwProbeCache.Unlock()
	return hwProbeCache.generation
}

// recordVerifiedHWDevice remembers the candidate a backend's smoke encode
// passed on, under the generation that was current when the probe began.
//
// generation is passed in rather than read here because the write happens after
// the probe: an invalidation that landed in between has already discarded this
// verdict, and filing it under the new generation would hand execution a device
// the re-probe was asked to re-verify. A stale generation is dropped instead,
// which reads as "nothing verified yet" — the same state a cold process is in,
// and the one the next walk repairs.
//
// NVENC with no configured device records nothing, because CUDA addresses its
// GPU without a path: there is nothing for execution to adopt.
func recordVerifiedHWDevice(generation uint64, backend, device string) {
	if device == "" {
		return
	}
	hwProbeCache.Lock()
	defer hwProbeCache.Unlock()
	if hwProbeCache.generation != generation {
		return
	}
	key := verifiedHWDeviceKey(generation, backend)
	if slices.Contains(hwProbeCache.verifiedDevices[key], device) {
		return
	}
	hwProbeCache.verifiedDevices[key] = append(hwProbeCache.verifiedDevices[key], device)
}

// VerifiedHWDevice returns the render device this process most recently
// verified the given backend on, or "" when no probe has passed for it.
//
// It exists because auto-detection and execution pick devices independently:
// detection walks a backend's candidates in order and stops at the first that
// passes a smoke encode, while a transcode with no configured playback.hw_device
// falls back to PickRenderDevice, which returns whatever sorts first under
// /dev/dri. On a host whose first render node belongs to a different vendor,
// those are different GPUs, and the transcode initializes hardware that was
// never verified. Reading the verified device closes that gap without making
// every caller of resolution carry a second return value.
func VerifiedHWDevice(backend string) string {
	devices := VerifiedHWDevices(backend)
	if len(devices) == 0 {
		return ""
	}
	return devices[0]
}

// VerifiedHWDevices returns every device this process has verified the given
// backend on, in probe order, or nil when no probe has passed for it.
//
// It has more than one entry only for a configured multi-device
// playback.hw_device: detection stops at the first pass when it is choosing a
// backend, but a configured list is *allocated* across, so every entry in it
// has to be probed or the inventory would vouch for cards nothing tested. The
// device balancer intersects its candidates with this set, which is what keeps
// a workload off a card that is present but broken.
func VerifiedHWDevices(backend string) []string {
	hwProbeCache.Lock()
	defer hwProbeCache.Unlock()
	return slices.Clone(hwProbeCache.verifiedDevices[verifiedHWDeviceKey(hwProbeCache.generation, backend)])
}

func verifiedHWDeviceKey(generation uint64, backend string) string {
	return strconv.FormatUint(generation, 10) + "\x00" + backend
}

// allocatesAcross reports whether every one of a backend's candidate devices is
// one execution may be handed, rather than an alternative detection chooses
// between.
//
// That is true only for a configured multi-device playback.hw_device, which is
// what accessible being non-nil marks. NVENC is never balanced across a list —
// acquireHWDevice hands it the first configured entry — and discovered
// candidates are alternatives, resolved to the single device VerifiedHWDevice
// reports.
func (c hwCandidates) allocatesAcross(backend string) bool {
	return backend != transcodeHWNVENC && c.accessible != nil && len(c.devicesFor(backend)) > 1
}

// unprobeableReason reports why a candidate cannot be smoke-encoded on, or
// false when it can be.
//
// Both reasons describe the *configuration*, not the hardware, which is why the
// caller records them as skipped rather than failed: neither is evidence that a
// card stopped working.
func (c hwCandidates) unprobeableReason(backend, device string) (string, bool) {
	if device == "" {
		// NVENC's CUDA default, and the only shape a discovered candidate takes.
		return "", false
	}
	if backend == transcodeHWNVENC {
		// A render node path is a perfectly good hw_device for QSV or VAAPI and
		// meaningless to CUDA. On a mixed host NVENC stays a candidate through
		// hasNVIDIADevice even while the node is deliberately configured for
		// QSV, and smoke-encoding CUDA against /dev/dri/renderD128 fails for a
		// reason that has nothing to do with the NVIDIA card being fine.
		if !isCUDADeviceIdentity(device) {
			return "configured hw_device is a render node path, not a CUDA index or GPU uuid", true
		}
		// Otherwise unconditionally probeable: a CUDA index or uuid is not a
		// file, so failing to open it is meaningless and only the smoke encode
		// can answer.
		return "", false
	}
	if c.accessible == nil {
		// Discovered candidates are openable by construction.
		return "", false
	}
	if c.accessible[device] {
		return "", false
	}
	return "device not accessible on this node", true
}

// isCUDADeviceIdentity reports whether a configured device names a GPU the way
// CUDA does — an index, a "cuda:N", or a GPU uuid — rather than a DRM render
// node path.
func isCUDADeviceIdentity(device string) bool {
	return !strings.ContainsRune(device, '/')
}

// deviceOpenable mirrors the accessibility filter listRenderDevices applies to
// discovered devices: a device this process cannot open cannot host a probe or
// a transcode.
func deviceOpenable(device string) bool {
	f, err := os.Open(device)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// hwProbeFailureReason attributes a failure to its device only when several
// candidates were tried; a single candidate reads better bare.
func hwProbeFailureReason(candidateCount int, device, reason string) string {
	if candidateCount < 2 || device == "" {
		return reason
	}
	return device + ": " + reason
}

// hwBackendProbe verifies one backend against an FFmpeg binary and candidate
// device. commandCount is the number of bounded commands the probe may run and
// budgets the shared deadline.
type hwBackendProbe struct {
	commandCount int
	run          func(ctx context.Context, ffmpegPath, device string, commandTimeout time.Duration) hwProbeResult
}

func hwBackendProbeFor(backend string) (hwBackendProbe, bool) {
	switch backend {
	case transcodeHWNVENC:
		return hwBackendProbe{commandCount: 4, run: probeFFmpegNVENCContext}, true
	case transcodeHWQSV:
		return hwBackendProbe{commandCount: 3, run: probeFFmpegQSVContext}, true
	case transcodeHWVAAPI:
		return hwBackendProbe{commandCount: 2, run: probeFFmpegVAAPIContext}, true
	default:
		return hwBackendProbe{}, false
	}
}

func ffmpegSupportsBackend(backend, ffmpegPath, device string) (bool, string) {
	return ffmpegSupportsBackendContext(context.Background(), backend, ffmpegPath, device)
}

// ffmpegSupportsBackendContext verifies one backend, coalescing concurrent cold
// probes and reusing a positive result for the process lifetime. A failure is
// retried once its short negative TTL expires, so a driver or binary repaired
// underneath a running server is picked up without a restart.
func ffmpegSupportsBackendContext(ctx context.Context, backend, ffmpegPath, device string) (bool, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	probe, ok := hwBackendProbeFor(backend)
	if !ok {
		return false, "unsupported hardware backend " + backend
	}
	ffmpegPath = normalizeFFmpegPath(ffmpegPath)
	// The flight below outlives an abandoned caller, so every test-mutable seam
	// it touches is snapshotted here rather than dereferenced inside it.
	commandTimeout := hwProbeCommandTimeout
	negativeTTL := hwProbeNegativeTTL
	now := hwProbeNow
	flightStarted := hwProbeFlightStarted
	hwProbeCache.Lock()
	// The generation is baked into the key, so an invalidation that lands while
	// this probe runs leaves the flight writing to a key nobody will read again
	// and sends the next caller to a fresh one.
	cacheKey := hwProbeCacheKey(hwProbeCache.generation, ffmpegPath, backend, device)
	if entry, ok := hwProbeCache.entries[cacheKey]; ok && hwProbeCacheEntryCurrent(entry, now()) {
		hwProbeCache.Unlock()
		return entry.result.available, entry.result.reason
	}
	hwProbeCache.Unlock()

	// Raised here, on the calling goroutine, and not inside the function below.
	// DoChan schedules that function on a new goroutine and returns without
	// waiting for it to run, so a caller whose context is already done takes the
	// ctx.Done() branch and returns while the probe has not reached its first
	// line. Anything that released its own claim on the encoder when this call
	// returned — the transcode node's capability build does exactly that — would
	// then hand a re-probe an encoder that is about to be busy. Registering
	// before the call closes the window: from here on the flight is counted
	// whether or not it has started, and whether or not anyone still waits for
	// it. See HWProbesInFlight.
	hwProbesInFlight.Add(1)
	resultCh := hwProbeCache.group.DoChan(cacheKey, func() (any, error) {
		hwProbeCache.Lock()
		cached, ok := hwProbeCache.entries[cacheKey]
		hwProbeCache.Unlock()
		if ok && hwProbeCacheEntryCurrent(cached, now()) {
			return cached.result, nil
		}
		if flightStarted != nil {
			flightStarted()
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), time.Duration(probe.commandCount)*commandTimeout+time.Second)
		defer cancel()
		result := probe.run(probeCtx, ffmpegPath, device, commandTimeout)
		entry := hwProbeCacheEntry{result: result}
		if !result.available {
			entry.expiresAt = now().Add(negativeTTL)
		}
		hwProbeCache.Lock()
		hwProbeCache.entries[cacheKey] = entry
		hwProbeCache.Unlock()
		return result, nil
	})
	select {
	case <-ctx.Done():
		// The flight outlives this caller by design, so the claim goes with it
		// rather than with us. DoChan's channel is buffered and every registered
		// waiter is served, so this receive always lands and the count always
		// comes back down.
		go func() {
			<-resultCh
			hwProbesInFlight.Add(-1)
		}()
		return false, ctx.Err().Error()
	case shared := <-resultCh:
		hwProbesInFlight.Add(-1)
		if shared.Err != nil {
			return false, shared.Err.Error()
		}
		result, ok := shared.Val.(hwProbeResult)
		if !ok {
			return false, "invalid shared hardware probe result"
		}
		return result.available, result.reason
	}
}

// hwProbesInFlight counts hardware smoke encodes running right now, including
// ones whose caller has already given up on them.
var hwProbesInFlight atomic.Int64

// HWProbesInFlight reports how many hardware smoke encodes this process has
// claimed the encoder for.
//
// A probe outlives its caller by design: the singleflight task runs on a
// background context so that a canceled request cannot kill work another
// request is waiting on. The consequence is that a component which released its
// own claim on the GPU when its call returned — the transcode node's capability
// build is exactly this — can leave ffmpeg on the card with nothing accounting
// for it. Anything that needs the encoder exclusively must add this to whatever
// else it counts as busy, or it will claim an encoder that is not free and
// publish the collision as a hardware failure.
//
// It counts claims rather than running processes, and deliberately errs high:
// the claim is taken before the probe is dispatched (so a caller can never
// return while its flight is unaccounted for) and released when the result
// lands, and callers that share one flight each hold one. A brief overcount
// costs an operator a 409 and a retry; an undercount costs a false hardware
// regression on a GPU that is fine.
func HWProbesInFlight() int {
	count := hwProbesInFlight.Load()
	if count < 0 {
		return 0
	}
	return int(count)
}

func hwProbeCacheEntryCurrent(entry hwProbeCacheEntry, now time.Time) bool {
	return entry.result.available || now.Before(entry.expiresAt)
}

// InvalidateHWProbeCache drops every cached backend verdict so the next
// detection walk re-runs its FFmpeg smoke encodes against live hardware.
//
// It exists because a positive verdict is cached for the whole process
// lifetime, which is right for routing — a GPU that encoded a frame does not
// stop being able to between two playback requests — but blind to the one event
// that legitimately changes the answer: an operator replacing a driver, moving
// a card, or changing device access underneath a running node. Without this the
// only way to re-verify is a restart.
//
// A probe already in flight is neither canceled nor discarded — canceling
// shared work would fail an unrelated playback request waiting on it — but it
// is superseded: bumping the generation moves every cache and singleflight key,
// so the in-flight probe stores its verdict where nothing will read it and the
// next caller starts a genuinely cold probe rather than joining the old flight.
// That is what makes the operator-facing re-probe honest; without it a re-probe
// racing a background capability fetch would republish the verdict it was asked
// to discard, and report "nothing changed".
func InvalidateHWProbeCache() {
	hwProbeCache.Lock()
	defer hwProbeCache.Unlock()
	hwProbeCache.generation++
	hwProbeCache.entries = make(map[string]hwProbeCacheEntry)
	hwProbeCache.verifiedDevices = make(map[string][]string)
	// The GPU identity listing goes too. A detection walk drops it on its own,
	// but this is exported and nothing here can require that a walk follows —
	// the sampler reads identities every few seconds between walks, and an
	// operator who re-probes has asked for that to be current now.
	resetNVIDIAGPUUUIDs()
	// VideoToolbox keeps its own cache, keyed by the FFmpeg binary's identity
	// rather than by generation, so it has to be cleared rather than superseded.
	// An operator re-probing a Mac is asking the same question everyone else is:
	// does this host still encode on hardware.
	videoToolboxProbes.Lock()
	videoToolboxProbes.byPath = make(map[string]videoToolboxProbeEntry)
	videoToolboxProbes.Unlock()
}

// hwProbeCacheKey separates results per invalidation generation, per backend,
// and per candidate device on top of the FFmpeg binary's identity.
func hwProbeCacheKey(generation uint64, ffmpegPath, backend, device string) string {
	return strings.Join([]string{strconv.FormatUint(generation, 10), ffmpegIdentityKey(ffmpegPath), backend, device}, "\x00")
}

// ffmpegIdentityKey invalidates cached capability results when an FFmpeg
// executable is replaced at the same configured path.
func ffmpegIdentityKey(ffmpegPath string) string {
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
	// Counted for the life of the smoke encode, not the life of the caller: the
	// goroutine below is rooted at Background so an abandoned request cannot
	// kill work another is waiting on, and it keeps ffmpeg on the card after
	// every caller has returned. Raised here, before the goroutine exists, so
	// the claim cannot be observed unraised by anyone this call returns to.
	hwProbesInFlight.Add(1)
	videoToolboxProbes.Unlock()

	commandTimeout := hwProbeCommandTimeout
	retryDelay := videoToolboxProbeRetryDelay
	started := videoToolboxProbeStarted
	go func() {
		if started != nil {
			started()
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout+time.Second)
		defer cancel()
		defer hwProbesInFlight.Add(-1)
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
//
// The invalidation generation leads it, for the same reason the other hardware
// probes bake it into theirs: clearing the cache alone does not supersede a
// probe that is already running. That call stays registered under its key, the
// rebuild joins it instead of starting a cold one, and its completion
// repopulates the map — so an operator's re-probe publishes the very verdict it
// was asked to discard. Moving the key leaves the old flight writing somewhere
// nobody will read.
func videoToolboxProbeCacheKey(execPath string) string {
	return strconv.FormatUint(hwProbeGeneration(), 10) + "\x00" + execPath + "\x00" + ffmpegIdentityKey(execPath)
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
		ResolveHWAccelWithFFmpeg(opts.HWAccel, opts.FFmpegPath, opts.HWDevice) == transcodeHWVideoToolbox {
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
			"-i", smokeEncodeSource,
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

// probeFFmpegNVENCContext verifies the CUDA decode, scaling, and encode path a
// NVENC transcode depends on. NVENC selects its GPU through CUDA, so the
// candidate device path is not part of the command line.
func probeFFmpegNVENCContext(ctx context.Context, ffmpegPath, device string, commandTimeout time.Duration) hwProbeResult {
	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-hwaccels"); err != nil {
		return hwProbeResult{reason: "hwaccels probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "cuda") {
		return hwProbeResult{reason: "cuda hwaccel unavailable"}
	}

	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-encoders"); err != nil {
		return hwProbeResult{reason: "encoders probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, encoderH264NVENC) {
		return hwProbeResult{reason: encoderUnavailableReason(encoderH264NVENC)}
	} else if !ffmpegOutputHasToken(output, "hevc_nvenc") {
		return hwProbeResult{reason: "hevc_nvenc encoder unavailable"}
	}

	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-filters"); err != nil {
		return hwProbeResult{reason: "filters probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, "scale_cuda") {
		return hwProbeResult{reason: "scale_cuda filter unavailable"}
	} else if !ffmpegOutputHasToken(output, "hwupload_cuda") {
		return hwProbeResult{reason: "hwupload_cuda filter unavailable"}
	}

	return smokeEncodeResult(ctx, ffmpegPath, transcodeHWNVENC, device, commandTimeout)
}

// probeFFmpegQSVContext verifies the VAAPI-derived QSV chain against a
// candidate Intel render device. Either hwaccel listing is enough: the chain
// initializes a VAAPI display and derives the QSV device from it.
func probeFFmpegQSVContext(ctx context.Context, ffmpegPath, device string, commandTimeout time.Duration) hwProbeResult {
	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-hwaccels"); err != nil {
		return hwProbeResult{reason: "hwaccels probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, transcodeHWQSV) && !ffmpegOutputHasToken(output, transcodeHWVAAPI) {
		return hwProbeResult{reason: "qsv and vaapi hwaccels unavailable"}
	}

	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-encoders"); err != nil {
		return hwProbeResult{reason: "encoders probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, encoderH264QSV) {
		return hwProbeResult{reason: encoderUnavailableReason(encoderH264QSV)}
	} else if !ffmpegOutputHasToken(output, "hevc_qsv") {
		return hwProbeResult{reason: "hevc_qsv encoder unavailable"}
	}

	return smokeEncodeResult(ctx, ffmpegPath, transcodeHWQSV, device, commandTimeout)
}

// probeFFmpegVAAPIContext verifies the generic VAAPI encode path. VAAPI is the
// last fallback and only needs H.264 encoding to be useful, so the listing gate
// stays narrower than QSV's.
func probeFFmpegVAAPIContext(ctx context.Context, ffmpegPath, device string, commandTimeout time.Duration) hwProbeResult {
	if output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, "-hide_banner", "-encoders"); err != nil {
		return hwProbeResult{reason: "encoders probe failed: " + FormatFFmpegProbeFailure(err, output)}
	} else if !ffmpegOutputHasToken(output, encoderH264VAAPI) {
		return hwProbeResult{reason: encoderUnavailableReason(encoderH264VAAPI)}
	}

	return smokeEncodeResult(ctx, ffmpegPath, transcodeHWVAAPI, device, commandTimeout)
}

// smokeEncodeResult runs the backend's bounded single-frame encode, which is
// the only step that exercises the driver rather than FFmpeg's build flags.
func smokeEncodeResult(ctx context.Context, ffmpegPath, backend, device string, commandTimeout time.Duration) hwProbeResult {
	output, err := runFFmpegProbe(ctx, commandTimeout, ffmpegPath, hardwareSmokeEncodeArgs(backend, device)...)
	if err != nil {
		return hwProbeResult{reason: hardwareEncoder(backend) + " smoke encode failed: " + FormatFFmpegProbeFailure(err, output)}
	}
	return hwProbeResult{available: true}
}

// H.264 encoder names FFmpeg reports for each hardware backend.
const (
	encoderH264QSV   = "h264_qsv"
	encoderH264VAAPI = "h264_vaapi"
	encoderH264NVENC = "h264_nvenc"
)

// encoderUnavailableReason reports that a probe's -encoders listing did not
// include the given encoder.
func encoderUnavailableReason(encoder string) string {
	return encoder + " encoder unavailable"
}

// hardwareEncoder returns the H.264 encoder paired with a backend.
func hardwareEncoder(backend string) string {
	switch backend {
	case transcodeHWQSV:
		return encoderH264QSV
	case transcodeHWVAAPI:
		return encoderH264VAAPI
	default:
		return encoderH264NVENC
	}
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
	Path string `json:"path"`
	// PCIAddress is the device's sysfs PCI slot (e.g. 0000:03:00.0). It is
	// stable across reboots for a card that stays in its slot, which /dev/dri
	// paths are not, so it — not Path — identifies the hardware.
	PCIAddress string `json:"pci_address,omitempty"`
	// GPUUUID is NVIDIA's own permanent GPU identity, reported only when
	// nvidia-smi is installed. It survives a card moving between slots and
	// hosts, so it outranks PCIAddress wherever both are present.
	GPUUUID     string `json:"gpu_uuid,omitempty"`
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

// RenderDeviceIdentity is a render device's hardware identity, without the
// probing that a full capability report performs.
type RenderDeviceIdentity struct {
	// Path is the render node, e.g. /dev/dri/renderD128.
	Path string
	// PCIAddress is the sysfs PCI slot, e.g. 0000:03:00.0.
	PCIAddress string
	// Vendor is "intel", "nvidia", "amd", or empty when sysfs names one we do
	// not recognize.
	Vendor string
}

// RenderDeviceIdentities enumerates this host's render devices with the sysfs
// identity of each.
//
// It exists for callers that need to correlate a device across surfaces —
// notably resource sampling, which learns about GPUs by PCI address from DRM
// fdinfo and has to name them the way the rest of the server does. It runs no
// ffmpeg probe and takes no lock: it is a sysfs read, cheap enough to call on a
// sampling interval, unlike DetectHWAccelWithFFmpeg.
func RenderDeviceIdentities() []RenderDeviceIdentity {
	if currentGOOS != linuxGOOS {
		return nil
	}
	devices := listRenderDevices(defaultDRIDir)
	identities := make([]RenderDeviceIdentity, 0, len(devices))
	for _, device := range devices {
		identities = append(identities, RenderDeviceIdentity{
			Path:       device,
			PCIAddress: renderDevicePCIAddress(device),
			Vendor:     renderDeviceVendor(device),
		})
	}
	return identities
}

// SamplerDeviceIdentities is RenderDeviceIdentities in the shape the resource
// sampler consumes, ready to hand to nodemetrics.Options.DeviceIdentities.
//
// It lives here rather than beside each caller because the conversion is one
// fact, not three: every process that samples resources — the API host and both
// node types — needs the same translation, and three copies would drift the
// moment DeviceIdentity gains a field. The dependency points this way on
// purpose: nodemetrics stays free of any playback import, which is why it takes
// the identities as a provider in the first place.
func SamplerDeviceIdentities() []nodemetrics.DeviceIdentity {
	devices := RenderDeviceIdentities()
	identities := make([]nodemetrics.DeviceIdentity, 0, len(devices))
	for _, device := range devices {
		identities = append(identities, nodemetrics.DeviceIdentity{
			Path:       device.Path,
			PCIAddress: device.PCIAddress,
			Vendor:     device.Vendor,
		})
	}
	return identities
}

// renderDeviceVendor maps a device's sysfs PCI vendor id to a short label.
func renderDeviceVendor(renderDevPath string) string {
	name := filepath.Base(renderDevPath)
	switch readSysfsID(filepath.Join(sysClassDRMDir, name, "device", "vendor")) {
	case "0x8086":
		return "intel"
	case "0x10de":
		return "nvidia"
	case "0x1002":
		return "amd"
	default:
		return ""
	}
}

// renderDeviceDetails describes every listed device.
func renderDeviceDetails(devices []string) []RenderDeviceInfo {
	details := make([]RenderDeviceInfo, 0, len(devices))
	for _, device := range devices {
		pciAddress := renderDevicePCIAddress(device)
		details = append(details, RenderDeviceInfo{
			Path:        device,
			PCIAddress:  pciAddress,
			GPUUUID:     renderDeviceGPUUUID(device, pciAddress),
			Description: describeRenderDevice(device),
		})
	}
	return details
}

// renderDevicePCIAddress resolves the sysfs device symlink behind a render node
// and returns its PCI slot. Best effort: an unresolvable link (a virtual or
// non-PCI device, a restricted sysfs) yields an empty address rather than an
// error, because a missing identity only weakens inventory, never breaks it.
func renderDevicePCIAddress(renderDevPath string) string {
	name := filepath.Base(renderDevPath)
	devicePath := filepath.Join(sysClassDRMDir, name, "device")
	// sysfs exposes this as a symlink into the PCI tree. Anything else is a
	// device with no PCI identity, and its own directory name would be "device".
	info, err := os.Lstat(devicePath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return ""
	}
	return filepath.Base(resolved)
}

// renderDeviceGPUUUID returns NVIDIA's permanent GPU identity for a render
// device. Only NVIDIA-vendor devices are looked up: no other vendor publishes
// such an id, so querying for them would only cost a subprocess.
func renderDeviceGPUUUID(renderDevPath, pciAddress string) string {
	if pciAddress == "" || !isNVIDIADevice(renderDevPath) {
		return ""
	}
	return nvidiaGPUUUIDsByPCIAddress()[normalizePCIAddress(pciAddress)]
}

// nvidiaSMIQueryTimeout bounds the single nvidia-smi invocation. A wedged
// driver makes nvidia-smi hang, and hardware inventory must not inherit that.
var nvidiaSMIQueryTimeout = 3 * time.Second

// nvidiaSMIQuery is the execution seam for the GPU uuid listing; tests replace
// it rather than installing a fake binary on PATH.
var nvidiaSMIQuery = runNVIDIASMIQuery

// nvidiaGPUUUIDs caches the nvidia-smi listing. Within one generation of the
// probe caches a second query could only cost a subprocess to learn the same
// answer, since GPU identities do not change under a running kernel.
//
// It lives for one detection walk, not for the process, which a sync.Once would
// make it. Everything that reads it is asking what hardware this host has right
// now: drift detection compares one walk's answer against the last one, and
// shared-GPU placement groups nodes by it. A listing that outlives its walk
// makes all of those describe a machine that no longer exists — an nvidia-smi
// missing or broken at first call is never asked again, a card swapped into the
// same slot keeps answering to its predecessor's uuid, and a card hot-removed
// from an NVIDIA-only node goes on being reported by every scheduled snapshot
// until someone re-probes by hand. On such a node that uuid is the card's only
// identity, so nothing else in the report would show it gone.
var nvidiaGPUUUIDs struct {
	mu     sync.Mutex
	loaded bool
	byPCI  map[string]string
}

// resetNVIDIAGPUUUIDs drops the cached listing so the next lookup re-queries.
func resetNVIDIAGPUUUIDs() {
	nvidiaGPUUUIDs.mu.Lock()
	defer nvidiaGPUUUIDs.mu.Unlock()
	nvidiaGPUUUIDs.loaded = false
	nvidiaGPUUUIDs.byPCI = nil
}

func runNVIDIASMIQuery(ctx context.Context) ([]byte, error) {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, path, "--query-gpu=uuid,pci.bus_id", "--format=csv,noheader").Output()
}

func nvidiaGPUUUIDsByPCIAddress() map[string]string {
	nvidiaGPUUUIDs.mu.Lock()
	defer nvidiaGPUUUIDs.mu.Unlock()
	if nvidiaGPUUUIDs.loaded {
		return nvidiaGPUUUIDs.byPCI
	}
	nvidiaGPUUUIDs.loaded = true
	ctx, cancel := context.WithTimeout(context.Background(), nvidiaSMIQueryTimeout)
	defer cancel()
	output, err := nvidiaSMIQuery(ctx)
	if err != nil {
		// Expected on every host without the NVIDIA toolkit installed, so this
		// stays at debug: the report is complete without it.
		slog.Debug("nvidia-smi gpu identity query unavailable", "component", "playback", "error", err)
		return nil
	}
	nvidiaGPUUUIDs.byPCI = parseNVIDIAGPUUUIDs(output)
	return nvidiaGPUUUIDs.byPCI
}

// nvidiaGPUUUIDList returns every uuid nvidia-smi reports, sorted and
// deduplicated, independent of whether the card has a readable render node.
func nvidiaGPUUUIDList() []string {
	byPCI := nvidiaGPUUUIDsByPCIAddress()
	if len(byPCI) == 0 {
		return nil
	}
	uuids := make([]string, 0, len(byPCI))
	for _, uuid := range byPCI {
		if uuid != "" && !slices.Contains(uuids, uuid) {
			uuids = append(uuids, uuid)
		}
	}
	slices.Sort(uuids)
	return uuids
}

// parseNVIDIAGPUUUIDs reads "csv,noheader" rows of "<uuid>, <pci bus id>" and
// keys them by normalized PCI address. Malformed rows are skipped.
func parseNVIDIAGPUUUIDs(output []byte) map[string]string {
	byPCI := make(map[string]string)
	for line := range strings.Lines(string(output)) {
		uuid, address, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		uuid = strings.TrimSpace(uuid)
		address = normalizePCIAddress(address)
		if uuid == "" || address == "" {
			continue
		}
		byPCI[address] = uuid
	}
	return byPCI
}

// normalizePCIAddress makes sysfs and nvidia-smi addresses comparable:
// sysfs prints a 16-bit domain (0000:03:00.0) and nvidia-smi a 32-bit one
// (00000000:03:00.0), and neither guarantees a case.
func normalizePCIAddress(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	domain, rest, ok := strings.Cut(address, ":")
	if !ok {
		return address
	}
	value, err := strconv.ParseUint(domain, 16, 64)
	if err != nil {
		return address
	}
	return fmt.Sprintf("%04x:%s", value, rest)
}

// detectBootID reads the kernel's per-boot identity. It is Linux-only and
// best effort: an empty value simply means device identities cannot be scoped
// to a boot on this host.
func detectBootID() string {
	if currentGOOS != linuxGOOS {
		return ""
	}
	data, err := os.ReadFile(procBootIDPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

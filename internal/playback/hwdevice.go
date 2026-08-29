package playback

import (
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Multi-GPU balancing: playback.hw_device accepts a comma-separated list of
// render devices (e.g. "/dev/dri/renderD128,/dev/dri/renderD129"). Every GPU
// workload (streaming sessions, prepared downloads, chapter thumbnails)
// resolves the list to exactly one concrete device via AcquireHWDevice
// immediately before launching ffmpeg, and releases it when that process has
// exited. Balancing is supported for the render-device accelerators (QSV and
// VAAPI) on a homogeneous device list; NVENC identifies GPUs by CUDA
// index/UUID rather than render-node path, so a multi-entry list falls back
// to its first entry. A single configured value keeps the historical
// pass-through contract for every accelerator, so existing deployments are
// unaffected — but it is still counted, because per-device session reporting
// has to work on the one-GPU node that most deployments are.

// HWDeviceSet is the parsed form of the playback.hw_device setting: an
// ordered list of device entries. Order is priority order — ties in load
// resolve to the earlier entry.
type HWDeviceSet struct {
	devices []string
}

// ParseHWDeviceSet splits a configured hw_device value into its device set,
// trimming whitespace and dropping empty entries.
func ParseHWDeviceSet(configured string) HWDeviceSet {
	if strings.TrimSpace(configured) == "" {
		return HWDeviceSet{}
	}
	parts := strings.Split(configured, ",")
	devices := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			devices = append(devices, trimmed)
		}
	}
	return HWDeviceSet{devices: devices}
}

// Empty reports whether no device is configured (auto-detection applies).
func (s HWDeviceSet) Empty() bool { return len(s.devices) == 0 }

// Multi reports whether more than one device is configured.
func (s HWDeviceSet) Multi() bool { return len(s.devices) > 1 }

// List returns the configured devices in priority order.
func (s HWDeviceSet) List() []string { return s.devices }

// First returns the first configured device, or "" when empty.
func (s HWDeviceSet) First() string {
	if len(s.devices) == 0 {
		return ""
	}
	return s.devices[0]
}

// hwDeviceStat reports whether a device path exists; overridable in tests.
var hwDeviceStat = func(path string) error {
	_, err := os.Stat(path)
	return err
}

// hwDeviceLoad tracks active GPU workloads per render device.
var hwDeviceLoad = struct {
	mu     sync.Mutex
	counts map[string]int
}{counts: map[string]int{}}

// DefaultNVENCDevice is the accounting key for an NVENC workload that named no
// device. NVENC addresses GPUs through the CUDA runtime rather than a render
// node, and ffmpeg defaults to CUDA device 0, so that is what an unqualified
// NVENC job is actually occupying.
const DefaultNVENCDevice = "cuda:0"

// HWDeviceLoadSnapshot returns a copy of the active workload count per device.
//
// This exists so node metrics can report how many of this process's GPU jobs
// are pinned to each device — a number no driver can supply, because the driver
// sees processes and not sessions. It is a copy taken under the same lock the
// allocator uses, so a reader never observes a half-applied reservation and
// never holds up one.
func HWDeviceLoadSnapshot() map[string]int {
	hwDeviceLoad.mu.Lock()
	defer hwDeviceLoad.mu.Unlock()
	counts := make(map[string]int, len(hwDeviceLoad.counts))
	for device, count := range hwDeviceLoad.counts {
		if count > 0 {
			counts[device] = count
		}
	}
	return counts
}

// nvencAccountingDevice is the key an NVENC workload is counted under.
//
// A configured multi-entry list resolves to its first entry, matching the
// device NVENC will actually use; an unconfigured value counts against the CUDA
// default. Accounting only — selection is untouched, because counting a
// workload and balancing on the count are different decisions and NVENC is
// deliberately excluded from the second.
//
// A bare CUDA index is rewritten to "cuda:N", because the count is only useful
// if it joins with the name resource sampling publishes for the same GPU, and
// that is "cuda:N" (or the render path, which is matched against the same
// index). Counting "1" would leave every explicitly-selected NVIDIA GPU
// reporting zero sessions while it transcodes. A GPU UUID passes through: the
// sampler knows nvidia-smi's UUID for each card and matches on it.
func nvencAccountingDevice(configured string) string {
	first := ParseHWDeviceSet(configured).First()
	if first == "" {
		return DefaultNVENCDevice
	}
	if index, err := strconv.Atoi(first); err == nil && index >= 0 {
		return "cuda:" + strconv.Itoa(index)
	}
	return first
}

// hwAccelBalancesRenderDevices reports whether the resolved acceleration mode
// selects GPUs by render-device path, which is what the balancer hands out.
// NVENC addresses GPUs by CUDA index/UUID and is deliberately excluded.
func hwAccelBalancesRenderDevices(hwAccel string) bool {
	return hwAccel == "qsv" || hwAccel == "vaapi"
}

// presentHWDevices filters a device list to the entries that exist, falling
// back to the first entry when none do so the failure mode stays
// deterministic (ffmpeg reports the missing device, matching the historical
// wrong-path behavior of an explicit single value).
func presentHWDevices(devices []string) []string {
	present := make([]string, 0, len(devices))
	for _, device := range devices {
		if hwDeviceStat(device) == nil {
			present = append(present, device)
		}
	}
	if len(present) == 0 {
		return devices[:1]
	}
	return present
}

// leastLoadedHWDeviceLocked picks the device with the fewest active
// workloads, preserving list order on ties. Callers must hold hwDeviceLoad.mu.
func leastLoadedHWDeviceLocked(present []string) string {
	best := present[0]
	for _, device := range present[1:] {
		if hwDeviceLoad.counts[device] < hwDeviceLoad.counts[best] {
			best = device
		}
	}
	return best
}

// newHWDeviceRelease returns an idempotent release for a reservation that has
// already been added to hwDeviceLoad.
func newHWDeviceRelease(device string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			hwDeviceLoad.mu.Lock()
			if hwDeviceLoad.counts[device] > 0 {
				hwDeviceLoad.counts[device]--
			}
			hwDeviceLoad.mu.Unlock()
		})
	}
}

// countHWDeviceWorkload records one active workload against a device without
// influencing selection. It is the accounting half of a reservation, used where
// the device was decided elsewhere (NVENC, which picks by CUDA index, and
// session restarts that keep their original device).
func countHWDeviceWorkload(device string) func() {
	hwDeviceLoad.mu.Lock()
	hwDeviceLoad.counts[device]++
	hwDeviceLoad.mu.Unlock()
	return newHWDeviceRelease(device)
}

// reserveConcreteHWDevice reserves a device that was selected for an earlier
// process in the same transcode session. Restarts keep device affinity rather
// than running the least-loaded selection again.
func reserveConcreteHWDevice(device string) func() {
	release := countHWDeviceWorkload(device)
	hwDeviceLoad.mu.Lock()
	count := hwDeviceLoad.counts[device]
	hwDeviceLoad.mu.Unlock()
	slog.Info("GPU workload device reserved", "device", device, "active_workloads", count)
	return release
}

var nvencMultiDeviceWarnOnce sync.Once

// AcquireHWDevice resolves the configured hw_device value to exactly one
// device for one GPU workload. resolvedHWAccel must already be resolved (no
// "auto"). The returned release must be called exactly once when the ffmpeg
// process for this workload has exited; it is idempotent and a no-op for
// workloads that were not counted (a software accelerator, or a GPU accelerator
// with no configured device to name).
//
//   - Empty value: returns "" so downstream auto-detection applies.
//   - Single value: passes through unchanged for every accelerator, and is
//     counted for the GPU accelerators, because a node with one GPU is the
//     common deployment and its sessions have to be reportable too.
//   - Multi-device value with QSV/VAAPI: reserves the present device with the
//     fewest active workloads (ties keep list order) until release.
//   - NVENC: selection is unchanged — the first entry, or empty for
//     auto-detection — but the workload is counted against the device it will
//     occupy so per-device reporting covers NVIDIA nodes too. NVENC addresses
//     GPUs by CUDA index/UUID rather than render-node path, so it is still
//     never balanced across a list.
//   - Any other accelerator: falls back to the first entry without counting.
func AcquireHWDevice(configured, resolvedHWAccel string) (string, func()) {
	device, _, release := acquireHWDevice(configured, resolvedHWAccel, "")
	return device, release
}

// acquireHWDevice applies the normal allocator while optionally excluding one
// previously failed render device when another present device is available.
// The selected device is still reserved through the same accounting path.
//
// It also returns the key the workload was counted under ("" when it was not
// counted), so a caller that outlives the ffmpeg process — a transcode session
// that restarts one — can re-count the replacement against the same device
// without restating the rule for which workloads count.
func acquireHWDevice(configured, resolvedHWAccel, avoidDevice string) (device, workloadDevice string, release func()) {
	noop := func() {}
	set := ParseHWDeviceSet(configured)
	if resolvedHWAccel == transcodeHWNVENC {
		if set.Multi() {
			nvencMultiDeviceWarnOnce.Do(func() {
				slog.Warn("multi-device hw_device is not supported with NVENC (devices are CUDA index/UUID, not render paths); using the first entry",
					"hw_device", configured, "using", set.First())
			})
		}
		accounted := nvencAccountingDevice(configured)
		return set.First(), accounted, countHWDeviceWorkload(accounted)
	}
	if !hwAccelBalancesRenderDevices(resolvedHWAccel) {
		// A software workload occupies no GPU, so counting it would both
		// misreport the device and skew the balancer that reads the counts.
		return set.First(), "", noop
	}
	if !set.Multi() {
		// Nothing to balance, but there is still a workload on a known device.
		// Selection and accounting are separate decisions: skipping the second
		// with the first is what left every single-GPU QSV/VAAPI node reporting
		// zero sessions beside a busy engine.
		if first := set.First(); first != "" {
			return first, first, countHWDeviceWorkload(first)
		}
		// No configured device. Resolving it here rather than leaving it to
		// PickRenderDevice downstream fixes two things at once: the workload
		// runs on the render node auto-detection actually verified this backend
		// on — not on whatever sorts first under /dev/dri, which on a
		// mixed-vendor host is a different GPU — and it becomes countable, so a
		// default-configured node stops reporting zero sessions beside a busy
		// engine. With no verified device (nothing probed yet, or a backend
		// named explicitly and never walked) this falls through unchanged and
		// ffmpeg picks one downstream exactly as before.
		if verified := VerifiedHWDevice(resolvedHWAccel); verified != "" {
			return verified, verified, countHWDeviceWorkload(verified)
		}
		// Nothing verified. That is not only the cold-process case: an
		// explicitly configured backend short-circuits resolution, so a host
		// running hw_accel=qsv with no hw_device never walks its hardware at all
		// and would otherwise never name a device here — reporting zero GPU
		// sessions for every transcode it runs. Fall back to the device
		// execution is about to pick anyway, which is this same first render
		// node; appendHWAccelArgs resolves an empty value the same way.
		if detected := detectRenderDevice(defaultDRIDir); detected != "" {
			return detected, detected, countHWDeviceWorkload(detected)
		}
		return "", "", noop
	}
	// Select and reserve in one critical section so concurrent workload starts
	// observe each other's reservations instead of piling onto one device.
	// The same ceiling the probe matrix stops at: nothing past it has a verdict
	// behind it, so nothing past it takes work. Applied to the parsed list in
	// the order it was configured, which is the order the walk probed, so both
	// sides keep exactly the same devices.
	present := verifiedHWDevices(resolvedHWAccel, presentHWDevices(UsableHWDevices(set.List())))
	if len(present) > 1 && avoidDevice != "" {
		eligible := make([]string, 0, len(present)-1)
		for _, device := range present {
			if device != avoidDevice {
				eligible = append(eligible, device)
			}
		}
		if len(eligible) > 0 {
			present = eligible
		}
	}
	hwDeviceLoad.mu.Lock()
	selected := leastLoadedHWDeviceLocked(present)
	hwDeviceLoad.counts[selected]++
	count := hwDeviceLoad.counts[selected]
	hwDeviceLoad.mu.Unlock()
	slog.Info("GPU workload device selected", "device", selected, "active_workloads", count)

	return selected, selected, newHWDeviceRelease(selected)
}

// verifiedHWDevices narrows a configured device list to the entries whose smoke
// encode actually passed.
//
// Presence is not fitness. A device that exists and can be opened can still fail
// to initialize the backend — a card in a bad state, a driver mismatch, a
// container that mapped the node without the matching libraries — and detection
// already found that out. Balancing across every present entry would hand a
// share of the node's workloads to that card and fail each of them at startup,
// while the capability report shows the backend verified.
//
// It narrows only when there is something to narrow to. An empty verified set
// means detection never ran for this backend (a cold process, or hw_accel named
// explicitly so resolution short-circuited), which is no evidence against any
// device, so the full present list stands.
func verifiedHWDevices(resolvedHWAccel string, present []string) []string {
	verified := VerifiedHWDevices(resolvedHWAccel)
	if len(verified) == 0 {
		return present
	}
	eligible := make([]string, 0, len(present))
	for _, device := range present {
		if slices.Contains(verified, device) {
			eligible = append(eligible, device)
		}
	}
	if len(eligible) == 0 {
		// Every present device failed its probe, or the probe set and the
		// configured set have drifted apart. Excluding everything would leave
		// the balancer with nothing to pick, which is worse than letting ffmpeg
		// try and report a real error.
		return present
	}
	return eligible
}

// hwDeviceActiveCount reports the active workload count for one device; test
// helper for asserting release boundaries.
func hwDeviceActiveCount(device string) int {
	hwDeviceLoad.mu.Lock()
	defer hwDeviceLoad.mu.Unlock()
	return hwDeviceLoad.counts[device]
}

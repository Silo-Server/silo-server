package playback

import (
	"log/slog"
	"os"
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
// unaffected.

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

// reserveConcreteHWDevice reserves a device that was selected for an earlier
// process in the same transcode session. Restarts keep device affinity rather
// than running the least-loaded selection again.
func reserveConcreteHWDevice(device string) func() {
	hwDeviceLoad.mu.Lock()
	hwDeviceLoad.counts[device]++
	count := hwDeviceLoad.counts[device]
	hwDeviceLoad.mu.Unlock()
	slog.Info("GPU workload device reserved", "device", device, "active_workloads", count)
	return newHWDeviceRelease(device)
}

var nvencMultiDeviceWarnOnce sync.Once

// AcquireHWDevice resolves the configured hw_device value to exactly one
// device for one GPU workload. resolvedHWAccel must already be resolved (no
// "auto"). The returned release must be called exactly once when the ffmpeg
// process for this workload has exited; it is idempotent and a no-op for
// workloads that did not reserve (empty or single-device value, or an
// accelerator the balancer does not manage).
//
//   - Empty value: returns "" so downstream auto-detection applies.
//   - Single value: passes through unchanged for every accelerator.
//   - Multi-device value with QSV/VAAPI: reserves the present device with the
//     fewest active workloads (ties keep list order) until release.
//   - Multi-device value with any other accelerator (including NVENC, which
//     addresses GPUs by CUDA index/UUID, not render-node path): falls back to
//     the first entry without reserving.
func AcquireHWDevice(configured, resolvedHWAccel string) (string, func()) {
	noop := func() {}
	set := ParseHWDeviceSet(configured)
	if !set.Multi() {
		return set.First(), noop
	}
	if !hwAccelBalancesRenderDevices(resolvedHWAccel) {
		if resolvedHWAccel == "nvenc" {
			nvencMultiDeviceWarnOnce.Do(func() {
				slog.Warn("multi-device hw_device is not supported with NVENC (devices are CUDA index/UUID, not render paths); using the first entry",
					"hw_device", configured, "using", set.First())
			})
		}
		return set.First(), noop
	}
	// Select and reserve in one critical section so concurrent workload starts
	// observe each other's reservations instead of piling onto one device.
	present := presentHWDevices(set.List())
	hwDeviceLoad.mu.Lock()
	device := leastLoadedHWDeviceLocked(present)
	hwDeviceLoad.counts[device]++
	count := hwDeviceLoad.counts[device]
	hwDeviceLoad.mu.Unlock()
	slog.Info("GPU workload device selected", "device", device, "active_workloads", count)

	return device, newHWDeviceRelease(device)
}

// hwDeviceActiveCount reports the active workload count for one device; test
// helper for asserting release boundaries.
func hwDeviceActiveCount(device string) int {
	hwDeviceLoad.mu.Lock()
	defer hwDeviceLoad.mu.Unlock()
	return hwDeviceLoad.counts[device]
}

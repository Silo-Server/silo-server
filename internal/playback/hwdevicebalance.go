package playback

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Multi-GPU balancing: playback.hw_device accepts a comma-separated list of
// render devices (e.g. "/dev/dri/renderD128,/dev/dri/renderD129"). Each
// transcode session resolves the list to one concrete device at spawn,
// picking the present device with the fewest active GPU sessions (ties keep
// list order), and holds that device until the session shuts down. A single
// configured value keeps the historical pass-through contract, so existing
// deployments are unaffected.

// hwDeviceStat reports whether a device path exists; overridable in tests.
var hwDeviceStat = func(path string) error {
	_, err := os.Stat(path)
	return err
}

// hwDeviceLoad tracks active GPU transcode sessions per render device.
var hwDeviceLoad = struct {
	mu     sync.Mutex
	counts map[string]int
}{counts: map[string]int{}}

// ParseHWDevices splits a configured hw_device value into its device list,
// trimming whitespace and dropping empty entries. A single bare value yields a
// one-element list; an empty value yields nil.
func ParseHWDevices(configured string) []string {
	if strings.TrimSpace(configured) == "" {
		return nil
	}
	parts := strings.Split(configured, ",")
	devices := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			devices = append(devices, trimmed)
		}
	}
	if len(devices) == 0 {
		return nil
	}
	return devices
}

// hwAccelUsesRenderDevice reports whether the resolved acceleration mode
// consumes a GPU render device worth balancing.
func hwAccelUsesRenderDevice(hwAccel string) bool {
	switch hwAccel {
	case "qsv", "vaapi", "nvenc":
		return true
	}
	return false
}

// selectLeastLoadedHWDevice picks the present device with the fewest active
// sessions from a parsed list, preserving list order on ties. When no listed
// device exists it falls back to the first entry so the failure mode stays
// deterministic (ffmpeg reports the missing device, matching the historical
// wrong-path behavior of an explicit single value).
func selectLeastLoadedHWDevice(devices []string) string {
	present := make([]string, 0, len(devices))
	for _, device := range devices {
		if hwDeviceStat(device) == nil {
			present = append(present, device)
		}
	}
	if len(present) == 0 {
		return devices[0]
	}
	hwDeviceLoad.mu.Lock()
	defer hwDeviceLoad.mu.Unlock()
	best := present[0]
	for _, device := range present[1:] {
		if hwDeviceLoad.counts[device] < hwDeviceLoad.counts[best] {
			best = device
		}
	}
	return best
}

// resolveSessionHWDevice resolves the configured hw_device value for one
// transcode session. Multi-device values balance least-loaded across present
// devices; single values pass through unchanged; empty stays empty (later
// auto-detection applies). The returned release must be called exactly once
// when the session ends; it is idempotent and a no-op for sessions that did
// not acquire (software accel, single/empty device value).
func resolveSessionHWDevice(configured, hwAccel string) (string, func()) {
	noop := func() {}
	devices := ParseHWDevices(configured)
	if len(devices) <= 1 {
		return configured, noop
	}
	device := selectLeastLoadedHWDevice(devices)
	if !hwAccelUsesRenderDevice(hwAccel) {
		return device, noop
	}
	hwDeviceLoad.mu.Lock()
	hwDeviceLoad.counts[device]++
	count := hwDeviceLoad.counts[device]
	hwDeviceLoad.mu.Unlock()
	slog.Info("transcode GPU selected", "device", device, "active_sessions", count)

	var once sync.Once
	release := func() {
		once.Do(func() {
			hwDeviceLoad.mu.Lock()
			if hwDeviceLoad.counts[device] > 0 {
				hwDeviceLoad.counts[device]--
			}
			hwDeviceLoad.mu.Unlock()
		})
	}
	return device, release
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

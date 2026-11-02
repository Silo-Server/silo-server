package tonemap

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// sysfsRoot is where DRM device facts are read; tests point it at a fixture tree.
var sysfsRoot = "/sys"

const intelPCIVendorID = "0x8086"

// vaapiFilter returns the tone-map filter the VAAPI backend uses for the
// configured devices. Intel's VAAPI tone mapper turns HLG teal and inverted and
// crushes PQ midtones (#2414), so when any device is an Intel GPU, VAAPI tone
// maps HDR on OpenCL with the graph QSV uses (IntelFilter). Other vendors keep
// tonemap_vaapi. One filter covers the whole device list, and FFmpeg maps VAAPI
// frames to OpenCL only on Intel, so a non-Intel device listed beside an Intel
// one fails the HDR smoke and HDR is offered on neither.
func vaapiFilter(hardwareDevice string) string {
	if slices.ContainsFunc(probeDevices(hardwareDevice, BackendVAAPI), isIntelDRMDevice) {
		return HardwareFilterOpenCL
	}
	return HardwareFilterVAAPI
}

func isIntelDRMDevice(device string) bool {
	return strings.EqualFold(drmVendor(device), intelPCIVendorID)
}

// drmVendor returns the PCI vendor ID of a render device, or "" when sysfs
// doesn't give one. sysfs is not namespaced, so a device that exists is looked
// up by its device number, never by name: a container may give another GPU's
// node the name renderD128, and a link may take several hops. Only a
// /dev/dri path that doesn't exist falls back to its node name.
func drmVendor(device string) string {
	if number, ok := charDeviceNumber(device); ok {
		return readSysfsVendor("dev", "char", number)
	}
	if strings.HasPrefix(device, "/dev/dri/") {
		return readSysfsVendor("class", "drm", drmNodeName(device))
	}
	return ""
}

// drmVendorUnreadable reports whether device exists but sysfs doesn't give its
// GPU vendor.
func drmVendorUnreadable(device string) bool {
	_, exists := charDeviceNumber(device)
	return exists && drmVendor(device) == ""
}

// unreadableVendorWarned latches the unreadable-vendor warning to one line per
// device: an incomplete probe reruns every negative-cache interval.
var unreadableVendorWarned sync.Map

// readSysfsVendor reads the PCI vendor of the sysfs device directory at dir
// under sysfsRoot.
func readSysfsVendor(dir ...string) string {
	data, err := os.ReadFile(filepath.Join(sysfsRoot, filepath.Join(dir...), "device", "vendor"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// drmNodeName returns the DRM node a device path names. A /dev/dri/by-path
// link names none itself, so it follows the link to its renderD node.
func drmNodeName(device string) string {
	if target, err := os.Readlink(device); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(device), target)
		}
		device = target
	}
	return filepath.Base(device)
}

package tonemap

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// sysfsRoot is where DRM device facts are read; tests point it at a fixture tree.
var sysfsRoot = "/sys"

const intelPCIVendorID = "0x8086"

// vaapiFilter returns the tone-map filter the VAAPI backend uses for the
// configured devices. Intel's VAAPI tone mapper turns HLG teal and inverted and
// crushes PQ midtones (#2414), so when any device is an Intel GPU, VAAPI tone
// maps HDR on OpenCL with the graph QSV uses (IntelFilter). Other vendors keep
// tonemap_vaapi. One filter covers the whole device list, so a non-Intel
// device listed beside an Intel one fails the OpenCL HDR smoke and the backend
// offers HDR on neither.
func vaapiFilter(hardwareDevice string) string {
	if slices.ContainsFunc(probeDevices(hardwareDevice, BackendVAAPI), isIntelDRMDevice) {
		return HardwareFilterOpenCL
	}
	return HardwareFilterVAAPI
}

func isIntelDRMDevice(device string) bool {
	return strings.EqualFold(drmVendor(device), intelPCIVendorID)
}

// drmVendor returns the PCI vendor ID of a /dev/dri device, or "" when the
// device isn't a DRM node or sysfs can't be read.
func drmVendor(device string) string {
	if !strings.HasPrefix(device, "/dev/dri/") {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(sysfsRoot, "class", "drm", drmNodeName(device), "device", "vendor"))
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

// VAAPIOpenCLInitArgs declares the OpenCL device a VAAPI graph derives from
// the VAAPI device alias. Only an HDR source tone mapped on OpenCL needs it;
// SDR bases convert on VAAPI alone, so they keep working on a host without an
// OpenCL runtime.
func VAAPIOpenCLInitArgs(alias, filter string, kind SourceKind) []string {
	if filter != HardwareFilterOpenCL || IsSDRSource(kind) {
		return nil
	}
	return []string{initHWDeviceFlag, "opencl=ocl@" + alias}
}

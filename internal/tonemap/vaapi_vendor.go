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

// vaapiHDRUnreliable reports whether device is an Intel GPU. Intel's VAAPI
// tone mapper turns HLG teal and darkens PQ (#2414), so the VAAPI backend
// offers it SDR sources only; QSV converts HDR on Intel through OpenCL.
func vaapiHDRUnreliable(device string) bool {
	if !strings.HasPrefix(device, "/dev/dri/") {
		return false
	}
	data, err := os.ReadFile(filepath.Join(sysfsRoot, "class", "drm", drmNodeName(device), "device", "vendor"))
	return err == nil && strings.EqualFold(strings.TrimSpace(string(data)), intelPCIVendorID)
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

// vaapiCandidateSourceKinds narrows kinds to the sources the VAAPI backend
// may convert on device.
func vaapiCandidateSourceKinds(kinds []SourceKind, device string) []SourceKind {
	if !vaapiHDRUnreliable(device) {
		return kinds
	}
	return slices.DeleteFunc(slices.Clone(kinds), func(kind SourceKind) bool {
		return !IsSDRSource(kind)
	})
}

// expectedHardwareSourceKinds is the standard set a complete probe of backend
// on hardwareDevice advertises.
func expectedHardwareSourceKinds(backend, hardwareDevice string) []SourceKind {
	kinds := StandardSourceKinds()
	if backend != BackendVAAPI {
		return kinds
	}
	for _, device := range probeDevices(hardwareDevice, backend) {
		kinds = vaapiCandidateSourceKinds(kinds, device)
	}
	return kinds
}

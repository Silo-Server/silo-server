package playback

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// addPCIRenderDevice models the sysfs layout a real render node has: the drm
// entry's "device" is a symlink into the PCI tree, and the vendor id lives on
// the far side of it.
func (e *hwAccelTestEnv) addPCIRenderDevice(t *testing.T, name, vendor, pciAddress string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.driDir, name), []byte{}, 0o600); err != nil {
		t.Fatalf("create render device: %v", err)
	}
	pciDir := filepath.Join(e.sysDir, "pci", pciAddress)
	if err := os.MkdirAll(pciDir, 0o755); err != nil {
		t.Fatalf("create pci dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pciDir, "vendor"), []byte(vendor+"\n"), 0o644); err != nil {
		t.Fatalf("write vendor file: %v", err)
	}
	drmDir := filepath.Join(e.sysDir, name)
	if err := os.MkdirAll(drmDir, 0o755); err != nil {
		t.Fatalf("create drm dir: %v", err)
	}
	if err := os.Symlink(pciDir, filepath.Join(drmDir, "device")); err != nil {
		t.Fatalf("link drm device: %v", err)
	}
}

func (e *hwAccelTestEnv) setBootID(t *testing.T, bootID string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "boot_id")
	if err := os.WriteFile(path, []byte(bootID+"\n"), 0o600); err != nil {
		t.Fatalf("write boot id: %v", err)
	}
	previous := procBootIDPath
	procBootIDPath = path
	t.Cleanup(func() { procBootIDPath = previous })
}

// stubNVIDIASMIQuery is stubNVIDIASMI for a query whose answer changes.
func stubNVIDIASMIQuery(t *testing.T, query func(context.Context) ([]byte, error)) {
	t.Helper()
	previous := nvidiaSMIQuery
	nvidiaSMIQuery = query
	resetNVIDIAUUIDCacheForTest()
	t.Cleanup(func() {
		nvidiaSMIQuery = previous
		resetNVIDIAUUIDCacheForTest()
	})
}

func resetNVIDIAUUIDCacheForTest() {
	resetNVIDIAGPUUUIDs()
}

func renderDeviceDetail(t *testing.T, info HWAccelInfo, path string) RenderDeviceInfo {
	t.Helper()
	for _, detail := range info.RenderDeviceDetails {
		if detail.Path == path {
			return detail
		}
	}
	t.Fatalf("no render device detail for %s in %+v", path, info.RenderDeviceDetails)
	return RenderDeviceInfo{}
}

// A device with no PCI symlink (virtual, or a restricted sysfs) reports no
// address rather than the literal directory name behind the lookup.
func TestDetectHWAccelOmitsPCIAddressWithoutSysfsLink(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	detail := renderDeviceDetail(t, DetectHWAccelWithFFmpeg("auto", ffmpeg.path, ""), filepath.Join(env.driDir, "renderD128"))

	if detail.PCIAddress != "" {
		t.Fatalf("PCIAddress = %q, want empty", detail.PCIAddress)
	}
}

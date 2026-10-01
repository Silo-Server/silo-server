package playback

import (
	"context"
	"errors"
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

// stubNVIDIASMI replaces the nvidia-smi invocation and clears its process-wide
// cache, which is otherwise computed once and would leak between tests.
func stubNVIDIASMI(t *testing.T, output string, err error) {
	t.Helper()
	previous := nvidiaSMIQuery
	nvidiaSMIQuery = func(context.Context) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return []byte(output), nil
	}
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

// The device paths under /dev/dri are assigned by enumeration order and move
// when hardware is added or removed. PCI address, GPU uuid and boot id are what
// let an operator (and the node inventory) say the GPU behind a path is still
// the same GPU.
func TestDetectHWAccelReportsHardwareIdentity(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addPCIRenderDevice(t, "renderD128", "0x10de", "0000:03:00.0")
	env.addPCIRenderDevice(t, "renderD129", "0x8086", "0000:04:00.0")
	env.setBootID(t, "2f6e7a8b-9c0d-4a2b-8c3d-5b2c1f0e1111")
	// nvidia-smi prints a 32-bit PCI domain in upper case; sysfs prints 16 bits.
	stubNVIDIASMI(t, "GPU-11112222-3333-4444-5555-666677778888, 00000000:03:00.0\n"+
		"GPU-99990000-1111-2222-3333-444455556666, 00000000:04:00.0\n", nil)
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	info := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, "")

	if info.BootID != "2f6e7a8b-9c0d-4a2b-8c3d-5b2c1f0e1111" {
		t.Fatalf("BootID = %q", info.BootID)
	}
	nvidia := renderDeviceDetail(t, info, filepath.Join(env.driDir, "renderD128"))
	if nvidia.PCIAddress != "0000:03:00.0" {
		t.Fatalf("nvidia PCIAddress = %q, want 0000:03:00.0", nvidia.PCIAddress)
	}
	if nvidia.GPUUUID != "GPU-11112222-3333-4444-5555-666677778888" {
		t.Fatalf("nvidia GPUUUID = %q", nvidia.GPUUUID)
	}
	intel := renderDeviceDetail(t, info, filepath.Join(env.driDir, "renderD129"))
	if intel.PCIAddress != "0000:04:00.0" {
		t.Fatalf("intel PCIAddress = %q, want 0000:04:00.0", intel.PCIAddress)
	}
	// nvidia-smi listed this address, but the device is Intel: attributing an
	// NVIDIA uuid to it would merge two distinct GPUs into one inventory entry.
	if intel.GPUUUID != "" {
		t.Fatalf("intel GPUUUID = %q, want empty", intel.GPUUUID)
	}
}

// A host without the NVIDIA toolkit is the common case, not a failure: the
// report must still describe the hardware it can see.
func TestDetectHWAccelOmitsGPUUUIDWhenNVIDIASMIUnavailable(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addPCIRenderDevice(t, "renderD128", "0x10de", "0000:03:00.0")
	stubNVIDIASMI(t, "", errors.New("exec: \"nvidia-smi\": executable file not found in $PATH"))
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	detail := renderDeviceDetail(t, DetectHWAccelWithFFmpeg("auto", ffmpeg.path, ""), filepath.Join(env.driDir, "renderD128"))

	if detail.PCIAddress != "0000:03:00.0" {
		t.Fatalf("PCIAddress = %q, want the sysfs address even without nvidia-smi", detail.PCIAddress)
	}
	if detail.GPUUUID != "" {
		t.Fatalf("GPUUUID = %q, want empty", detail.GPUUUID)
	}
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

func TestDetectHWAccelOmitsBootIDOffLinux(t *testing.T) {
	env := setupHWAccelTest(t)
	env.setBootID(t, "2f6e7a8b-9c0d-4a2b-8c3d-5b2c1f0e1111")
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	if got := DetectHWAccelWithFFmpeg("auto", ffmpeg.path, "").BootID; got != "" {
		t.Fatalf("BootID = %q, want empty off Linux", got)
	}
}

func TestNormalizePCIAddress(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0000:03:00.0", "0000:03:00.0"},
		{"00000000:03:00.0", "0000:03:00.0"},
		{"00000000:0A:00.0", "0000:0a:00.0"},
		{" 0000:03:00.0 ", "0000:03:00.0"},
		{"not-an-address", "not-an-address"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizePCIAddress(tt.in); got != tt.want {
			t.Fatalf("normalizePCIAddress(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseNVIDIAGPUUUIDsSkipsMalformedRows(t *testing.T) {
	parsed := parseNVIDIAGPUUUIDs([]byte("GPU-aaa, 00000000:03:00.0\n\nmissing-separator\n, 00000000:05:00.0\nGPU-bbb, \n"))
	if len(parsed) != 1 {
		t.Fatalf("parsed = %v, want exactly the one well-formed row", parsed)
	}
	if parsed["0000:03:00.0"] != "GPU-aaa" {
		t.Fatalf("parsed = %v", parsed)
	}
}

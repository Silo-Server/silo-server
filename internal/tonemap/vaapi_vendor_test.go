package tonemap

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// TestMain keeps probe tests from depending on the GPU of the machine that
// runs them: without a fixture, no DRM device reports a vendor.
func TestMain(m *testing.M) {
	sysfsRoot = filepath.Join(os.TempDir(), "silo-tonemap-test-no-sysfs")
	os.Exit(m.Run())
}

// fakeDRMVendors points sysfsRoot at a tree where each named DRM node reports
// its vendor.
func fakeDRMVendors(t *testing.T, vendors map[string]string) {
	t.Helper()
	root := t.TempDir()
	for node, vendor := range vendors {
		dirs := []string{filepath.Join(root, "class", "drm", node, "device")}
		// A host that has this node looks it up by device number.
		if number, ok := charDeviceNumber("/dev/dri/" + node); ok {
			dirs = append(dirs, filepath.Join(root, "dev", "char", number, "device"))
		}
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "vendor"), []byte(vendor+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	previous := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = previous })
}

// vaapiProbeRunner accepts every command, lists the given filters, and records
// each smoke command it runs.
func vaapiProbeRunner(filters string, commands *[]string) CommandRunner {
	var mu sync.Mutex
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "-filters"):
			return []byte(filters), nil
		case slices.Contains(args, "-encoders"):
			return []byte("libx264 h264_vaapi"), nil
		}
		mu.Lock()
		*commands = append(*commands, strings.Join(args, " "))
		mu.Unlock()
		return nil, nil
	}
}

const vaapiProbeFilters = " .S. zscale V->V\n .S. tonemapx V->V\n .S. sidedata V->V\n .S. tonemap_vaapi V->V\n .S. scale_vaapi V->V\n" +
	" .S. tonemap_opencl V->V\n .S. hwmap V->V\n .S. overlay_opencl VV->V\n .S. scale_opencl V->V\n"

// TestProbeIntelVAAPIToneMapsHDROnOpenCL covers #2414: Intel's VAAPI tone
// mapper gives wrong colors for HLG and PQ, so VAAPI on an Intel GPU tone maps
// on OpenCL, as QSV does, and other vendors keep tonemap_vaapi.
func TestProbeIntelVAAPIToneMapsHDROnOpenCL(t *testing.T) {
	for _, tc := range []struct {
		name, vendor, wantFilter string
		wantDolbyVision          bool
	}{
		{name: "intel", vendor: "0x8086", wantFilter: HardwareFilterOpenCL, wantDolbyVision: true},
		{name: "amd", vendor: "0x1002", wantFilter: HardwareFilterVAAPI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDRMVendors(t, map[string]string{"renderD128": tc.vendor})
			var commands []string
			got := ProbeWithRunner(context.Background(), "/ffmpeg", BackendVAAPI, "/dev/dri/renderD128", vaapiProbeRunner(vaapiProbeFilters, &commands))
			if filter := got.FilterFor(ModeHardware, SourceHLG); filter != tc.wantFilter {
				t.Fatalf("VAAPI hardware filter = %q, want %q: %#v", filter, tc.wantFilter, got)
			}
			for _, kind := range StandardSourceKinds() {
				if !got.Supports(ModeHardware, kind) {
					t.Fatalf("VAAPI does not advertise %s: %#v", kind, got)
				}
			}
			if got.Supports(ModeHardware, SourceDolbyVision) != tc.wantDolbyVision {
				t.Fatalf("VAAPI Dolby Vision support = %t, want %t", !tc.wantDolbyVision, tc.wantDolbyVision)
			}
			if !probeCapabilitiesComplete(got, BackendVAAPI) {
				t.Fatalf("probe result counted as incomplete: %#v", got)
			}
			for _, command := range commands {
				if !strings.Contains(command, "h264_vaapi") {
					continue
				}
				openCL := strings.Contains(command, "opencl=ocl@va")
				sdr := strings.Contains(command, "color_trc=bt709:")
				if tc.wantFilter == HardwareFilterOpenCL {
					if strings.Contains(command, HardwareFilterVAAPI) {
						t.Fatalf("Intel smoke ran tonemap_vaapi: %s", command)
					}
					// SDR bases convert on VAAPI alone and must not need an
					// OpenCL runtime.
					if openCL == sdr {
						t.Fatalf("Intel smoke OpenCL device = %t for SDR = %t: %s", openCL, sdr, command)
					}
				} else if openCL || strings.Contains(command, HardwareFilterOpenCL) {
					t.Fatalf("non-Intel smoke used OpenCL: %s", command)
				}
			}
		})
	}
}

// TestProbeIntelVAAPIRequiresOpenCLFilters keeps an FFmpeg build without the
// OpenCL graph's filters from offering hardware tone mapping on Intel VAAPI,
// where tonemap_vaapi alone would give wrong colors.
func TestProbeIntelVAAPIRequiresOpenCLFilters(t *testing.T) {
	fakeDRMVendors(t, map[string]string{"renderD128": "0x8086"})
	var commands []string
	filters := " .S. zscale V->V\n .S. tonemapx V->V\n .S. sidedata V->V\n .S. tonemap_vaapi V->V\n .S. scale_vaapi V->V\n"
	got := ProbeWithRunner(context.Background(), "/ffmpeg", BackendVAAPI, "/dev/dri/renderD128", vaapiProbeRunner(filters, &commands))
	if slices.ContainsFunc(got, func(capability Capability) bool { return capability.Mode == ModeHardware }) {
		t.Fatalf("Intel VAAPI without OpenCL filters advertised hardware tone mapping: %#v", got)
	}
	if !got.Supports(ModeSoftware, SourcePQ) {
		t.Fatalf("software tone mapping lost PQ: %#v", got)
	}
}

// TestVAAPIFilterUsesOpenCLWhenAnyDeviceIsIntel covers a device list mixing
// vendors: one filter serves every device, and tonemap_vaapi is wrong on the
// Intel one.
func TestVAAPIFilterUsesOpenCLWhenAnyDeviceIsIntel(t *testing.T) {
	fakeDRMVendors(t, map[string]string{"renderD128": "0x1002", "renderD129": "0x8086"})
	for devices, want := range map[string]string{
		"/dev/dri/renderD128":                     HardwareFilterVAAPI,
		"/dev/dri/renderD128,/dev/dri/renderD129": HardwareFilterOpenCL,
		"": HardwareFilterVAAPI,
	} {
		if got := vaapiFilter(devices); got != want {
			t.Fatalf("vaapiFilter(%q) = %q, want %q", devices, got, want)
		}
	}
}

// TestDRMNodeNameFollowsByPathLinks lets a /dev/dri/by-path device find its
// node's vendor.
func TestDRMNodeNameFollowsByPathLinks(t *testing.T) {
	byPath := filepath.Join(t.TempDir(), "by-path")
	if err := os.MkdirAll(byPath, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(byPath, "pci-0000:03:00.0-render")
	if err := os.Symlink("../renderD129", link); err != nil {
		t.Fatal(err)
	}
	for device, want := range map[string]string{
		link:                  "renderD129",
		"/dev/dri/renderD128": "renderD128",
	} {
		if got := drmNodeName(device); got != want {
			t.Fatalf("drmNodeName(%q) = %q, want %q", device, got, want)
		}
	}
}

// TestCharDeviceNumberReadsTheNode looks a node up by the device it is, not by
// its name, which a container can give to another GPU.
func TestCharDeviceNumberReadsTheNode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("device numbers are read only on Linux")
	}
	if got, ok := charDeviceNumber("/dev/null"); !ok || got != "1:3" {
		t.Fatalf("charDeviceNumber(/dev/null) = %q, %t; want 1:3", got, ok)
	}
	for _, path := range []string{"/dev/dri/silo-missing-node", t.TempDir()} {
		if got, ok := charDeviceNumber(path); ok {
			t.Fatalf("charDeviceNumber(%q) = %q, want none", path, got)
		}
	}

	// An existing device reads its vendor by number and never by name.
	root := t.TempDir()
	for dir, vendor := range map[string]string{"dev/char/1:3": "0x8086", "class/drm/null": "0x1002"} {
		if err := os.MkdirAll(filepath.Join(root, dir, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "device", "vendor"), []byte(vendor+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = previous })
	if got := drmVendor("/dev/null"); got != "0x8086" {
		t.Fatalf("drmVendor(/dev/null) = %q, want the vendor under its device number", got)
	}
}

// TestDRMVendorUnreadableKeepsVAAPIFilter covers a host whose sysfs can't be
// read: no vendor is reported and VAAPI keeps tonemap_vaapi, as before.
func TestDRMVendorUnreadableKeepsVAAPIFilter(t *testing.T) {
	if got := drmVendor("/dev/dri/renderD128"); got != "" {
		t.Fatalf("drmVendor without sysfs = %q, want empty", got)
	}
	if got := vaapiFilter("/dev/dri/renderD128"); got != HardwareFilterVAAPI {
		t.Fatalf("vaapiFilter without sysfs = %q, want %q", got, HardwareFilterVAAPI)
	}
}

// TestVAAPIOpenCLPreflightUsesTheOpenCLGraph keeps the source preflight on the
// graph playback runs: an Intel VAAPI recipe frozen with tonemap_opencl
// declares the OpenCL device and never reaches tonemap_vaapi.
func TestVAAPIOpenCLPreflightUsesTheOpenCLGraph(t *testing.T) {
	request := SourcePreflightRequest{Mode: ModeHardware, Backend: BackendVAAPI, Filter: HardwareFilterOpenCL, Kind: SourceHLG, InputPath: "/media/in.mkv", HardwareDevice: "/dev/dri/renderD128"}
	args := strings.Join(sourceConversionPreflightArgs(request, 10, ""), " ")
	for _, want := range []string{"-init_hw_device vaapi=va:/dev/dri/renderD128 -init_hw_device opencl=ocl@va", "tonemap_opencl=", IntelOpenCLReturnFilter(), "-c:v h264_vaapi"} {
		if !strings.Contains(args, want) {
			t.Fatalf("preflight args lack %q: %s", want, args)
		}
	}
	if strings.Contains(args, HardwareFilterVAAPI) || strings.Contains(args, QSVInteropFilter()) {
		t.Fatalf("preflight args reach tonemap_vaapi or the QSV interop: %s", args)
	}
	request.SoftwareVideoDecode = true
	if filter := sourceConversionPreflightFilter(request); !strings.HasPrefix(filter, "format=p010le,hwupload,") {
		t.Fatalf("software-decoded HDR preflight upload = %q, want a 10-bit upload", filter)
	}
}

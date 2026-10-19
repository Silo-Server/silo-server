package tonemap

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMain keeps probe tests from depending on the GPU of the machine that
// runs them: without a fixture, no DRM device reports a vendor.
func TestMain(m *testing.M) {
	sysfsRoot = filepath.Join(os.TempDir(), "silo-tonemap-test-no-sysfs")
	os.Exit(m.Run())
}

// fakeDRMVendor points sysfsRoot at a tree where renderD128 reports vendor.
func fakeDRMVendor(t *testing.T, vendor string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "class", "drm", "renderD128", "device")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor"), []byte(vendor+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = previous })
}

// vaapiProbeRunner accepts every command and records whether a VAAPI HDR
// tone-map smoke ran.
func vaapiProbeRunner(hdrHardwareSmoke *bool) CommandRunner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case slices.Contains(args, "-filters"):
			return []byte(" .S. zscale V->V\n .S. tonemapx V->V\n .S. sidedata V->V\n .S. tonemap_vaapi V->V\n .S. scale_vaapi V->V\n"), nil
		case slices.Contains(args, "-encoders"):
			return []byte("libx264 h264_vaapi"), nil
		case strings.Contains(joined, "h264_vaapi") && strings.Contains(joined, HardwareFilterVAAPI):
			*hdrHardwareSmoke = true
		}
		return nil, nil
	}
}

// TestProbeOffersIntelVAAPISDRSourcesOnly covers #2414: Intel's VAAPI tone
// mapper gives wrong colors for HLG and PQ, so VAAPI on an Intel GPU must not
// advertise HDR sources, and other vendors keep them.
func TestProbeOffersIntelVAAPISDRSourcesOnly(t *testing.T) {
	hdr := []SourceKind{SourcePQ, SourceHLG, SourceHLGBT709}
	sdr := []SourceKind{SourceSDRBT709, SourceSDRBT2020}
	for _, tc := range []struct {
		name, vendor  string
		wantHDR       bool
		wantHDRSmokes bool
	}{
		{name: "intel", vendor: "0x8086"},
		{name: "amd", vendor: "0x1002", wantHDR: true, wantHDRSmokes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDRMVendor(t, tc.vendor)
			hdrSmoke := false
			got := ProbeWithRunner(context.Background(), "/ffmpeg", BackendVAAPI, "/dev/dri/renderD128", vaapiProbeRunner(&hdrSmoke))
			for _, kind := range sdr {
				if !got.Supports(ModeHardware, kind) {
					t.Fatalf("VAAPI does not advertise SDR source %s: %#v", kind, got)
				}
			}
			for _, kind := range hdr {
				if got.Supports(ModeHardware, kind) != tc.wantHDR {
					t.Fatalf("VAAPI hardware %s support = %t, want %t: %#v", kind, !tc.wantHDR, tc.wantHDR, got)
				}
				if !got.Supports(ModeSoftware, kind) {
					t.Fatalf("software tone mapping lost %s: %#v", kind, got)
				}
			}
			if hdrSmoke != tc.wantHDRSmokes {
				t.Fatalf("VAAPI HDR smoke ran = %t, want %t", hdrSmoke, tc.wantHDRSmokes)
			}
			if !probeCapabilitiesComplete(got, BackendVAAPI, "/dev/dri/renderD128") {
				t.Fatalf("probe result counted as incomplete: %#v", got)
			}
		})
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

// TestProbeIntelVAAPIResultDoesNotExpire keeps the narrowed Intel result
// cached, so the GPU smoke matrix doesn't rerun every negative-cache interval.
func TestProbeIntelVAAPIResultDoesNotExpire(t *testing.T) {
	resetProbeCache(t)
	fakeDRMVendor(t, "0x8086")
	now := time.Unix(100, 0)
	hdrSmoke := false
	calls := 0
	base := vaapiProbeRunner(&hdrSmoke)
	runner := func(ctx context.Context, path string, args ...string) ([]byte, error) {
		calls++
		return base(ctx, path, args...)
	}
	if _, err := probeCached(context.Background(), "/ffmpeg-intel-vaapi", BackendVAAPI, "/dev/dri/renderD128", runner, func() time.Time { return now }); err != nil {
		t.Fatalf("probe error = %v", err)
	}
	firstCalls := calls
	now = now.Add(24 * time.Hour)
	if _, err := probeCached(context.Background(), "/ffmpeg-intel-vaapi", BackendVAAPI, "/dev/dri/renderD128", runner, func() time.Time { return now }); err != nil {
		t.Fatalf("cached probe error = %v", err)
	}
	if calls != firstCalls {
		t.Fatalf("Intel VAAPI probe reran: calls = %d, want %d", calls, firstCalls)
	}
}

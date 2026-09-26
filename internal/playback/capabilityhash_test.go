package playback

import (
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func sampleCapabilityInfo() HWAccelInfo {
	return HWAccelInfo{
		Resolved:      "nvenc",
		BootID:        "5b2c1f0e-1111-4a2b-9c3d-2f6e7a8b9c0d",
		RenderDevices: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
		RenderDeviceDetails: []RenderDeviceInfo{
			{Path: "/dev/dri/renderD128", PCIAddress: "0000:03:00.0", GPUUUID: "GPU-aaa", Description: "NVIDIA GPU (0x2204)"},
			{Path: "/dev/dri/renderD129", PCIAddress: "0000:04:00.0", Description: "Intel GPU (0x9a49)"},
		},
		DetectedBackends: []DetectedBackend{
			{Backend: "nvenc", Verified: true, Devices: []string{"/dev/dri/renderD128"}},
			{Backend: "vaapi", Verified: false, Devices: []string{"/dev/dri/renderD129", "/dev/dri/renderD128"}, Reason: "h264_vaapi encoder unavailable"},
		},
		Transformations: []TransformationV3{
			{Name: "tone_map", Executor: "node", RecipeVersion: "v2", ValidatedClaims: []string{"hdr10", "hlg"}},
			{Name: "audio_to_aac", Executor: "node", RecipeVersion: "v1"},
		},
		TransportFeatures: []string{"feature-b", "feature-a"},
		ToneMapCapabilities: tonemap.Capabilities{
			{Mode: tonemap.ModeHardware, Backend: "nvenc", Filter: "tonemap_cuda", SourceKinds: []tonemap.SourceKind{"hdr10", "hlg"}},
			{Mode: tonemap.ModeSoftware, Backend: "software", Filter: "tonemap", SourceKinds: []tonemap.SourceKind{"hdr10"}},
		},
	}
}

// A node reports the same hardware twice with its slices enumerated in a
// different order — probe order and directory listing order are incidental. If
// that moved the hash, every sweep would look like a hardware change and refetch
// the whole inventory forever.
func TestComputeCapabilityHashIgnoresSliceOrder(t *testing.T) {
	info := sampleCapabilityInfo()
	shuffled := sampleCapabilityInfo()
	shuffled.RenderDevices = []string{"/dev/dri/renderD129", "/dev/dri/renderD128"}
	shuffled.RenderDeviceDetails = []RenderDeviceInfo{shuffled.RenderDeviceDetails[1], shuffled.RenderDeviceDetails[0]}
	shuffled.DetectedBackends = []DetectedBackend{shuffled.DetectedBackends[1], shuffled.DetectedBackends[0]}
	shuffled.DetectedBackends[0].Devices = []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}
	shuffled.Transformations = []TransformationV3{shuffled.Transformations[1], shuffled.Transformations[0]}
	shuffled.Transformations[1].ValidatedClaims = []string{"hlg", "hdr10"}
	shuffled.TransportFeatures = []string{"feature-a", "feature-b"}
	shuffled.ToneMapCapabilities = tonemap.Capabilities{shuffled.ToneMapCapabilities[1], shuffled.ToneMapCapabilities[0]}
	shuffled.ToneMapCapabilities[1].SourceKinds = []tonemap.SourceKind{"hlg", "hdr10"}

	if got, want := ComputeCapabilityHash(shuffled), ComputeCapabilityHash(info); got != want {
		t.Fatalf("reordered report hashed differently:\n got %s\nwant %s", got, want)
	}
}

// Fields that vary with who asked must not move the hash: otherwise the same
// node hashes differently depending on the caller.
func TestComputeCapabilityHashIgnoresPerCallerMetadata(t *testing.T) {
	info := sampleCapabilityInfo()
	want := ComputeCapabilityHash(info)

	info.Source = "remote"
	info.NodeURL = "http://node-7:8080"
	info.CapabilityHash = "sha256:stale"

	if got := ComputeCapabilityHash(info); got != want {
		t.Fatalf("report metadata changed the hash:\n got %s\nwant %s", got, want)
	}
}

// The advertised probe budget does move it, though it describes the report
// rather than the hardware. The control plane sizes real deadlines from the
// stored copy, so a node upgraded to a build that needs longer — changing
// nothing else about itself — has to reach the sweep, or the API keeps
// canceling that node's re-probes against a budget it has outgrown.
func TestComputeCapabilityHashTracksTheAdvertisedProbeBudget(t *testing.T) {
	info := sampleCapabilityInfo()
	info.ProbeRequestTimeoutMillis = 111_000
	before := ComputeCapabilityHash(info)

	info.ProbeRequestTimeoutMillis = 136_000
	if got := ComputeCapabilityHash(info); got == before {
		t.Fatal("a node that raised its advertised probe budget hashed identically; the sweep would never refetch it")
	}
}

func TestComputeCapabilityHashIsPrefixedSHA256(t *testing.T) {
	hash := ComputeCapabilityHash(sampleCapabilityInfo())
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("hash = %q, want a sha256: prefix", hash)
	}
	if len(hash) != len("sha256:")+64 {
		t.Fatalf("hash = %q, want 64 hex digits after the prefix", hash)
	}
}

// Every hardware or capability change must move the hash, since the hash is the
// only thing the health sweep looks at before deciding nothing changed.
func TestComputeCapabilityHashDetectsRealChanges(t *testing.T) {
	base := ComputeCapabilityHash(sampleCapabilityInfo())
	tests := []struct {
		name   string
		mutate func(*HWAccelInfo)
	}{
		{"resolved backend", func(i *HWAccelInfo) { i.Resolved = "vaapi" }},
		{"boot id", func(i *HWAccelInfo) { i.BootID = "0000ffff-2222-4a2b-9c3d-2f6e7a8b9c0d" }},
		{"render device removed", func(i *HWAccelInfo) {
			i.RenderDevices = i.RenderDevices[:1]
			i.RenderDeviceDetails = i.RenderDeviceDetails[:1]
		}},
		{"pci address", func(i *HWAccelInfo) { i.RenderDeviceDetails[0].PCIAddress = "0000:07:00.0" }},
		{"gpu uuid", func(i *HWAccelInfo) { i.RenderDeviceDetails[0].GPUUUID = "GPU-bbb" }},
		{"device description", func(i *HWAccelInfo) { i.RenderDeviceDetails[1].Description = "AMD GPU" }},
		{"backend verification lost", func(i *HWAccelInfo) { i.DetectedBackends[0].Verified = false }},
		{"backend failure reason", func(i *HWAccelInfo) { i.DetectedBackends[1].Reason = "no driver" }},
		{"verified device", func(i *HWAccelInfo) { i.DetectedBackends[0].Device = "/dev/dri/renderD129" }},
		{"transformation recipe version", func(i *HWAccelInfo) { i.Transformations[0].RecipeVersion = "v3" }},
		{"validated claims", func(i *HWAccelInfo) { i.Transformations[0].ValidatedClaims = []string{"hdr10"} }},
		{"transport feature", func(i *HWAccelInfo) { i.TransportFeatures = []string{"feature-a"} }},
		{"tone map filter", func(i *HWAccelInfo) { i.ToneMapCapabilities[0].Filter = "tonemap_opencl" }},
		{"tone map source kinds", func(i *HWAccelInfo) {
			i.ToneMapCapabilities[0].SourceKinds = []tonemap.SourceKind{"hdr10"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := sampleCapabilityInfo()
			tt.mutate(&info)
			if got := ComputeCapabilityHash(info); got == base {
				t.Fatalf("%s did not change the hash (%s)", tt.name, got)
			}
		})
	}
}

// An empty report still hashes, so a node with no hardware advertises a stable
// identity rather than an empty one the sweep would read as "cannot say".
func TestComputeCapabilityHashOfEmptyReport(t *testing.T) {
	hash := ComputeCapabilityHash(HWAccelInfo{})
	if hash == "" {
		t.Fatal("empty report produced no hash")
	}
	if hash == ComputeCapabilityHash(sampleCapabilityInfo()) {
		t.Fatal("empty report hashed the same as a populated one")
	}
}

// The ceiling on a node-advertised budget has to sit above what a real
// configuration asks for. Picked as a round five minutes it was already below a
// nine-device node's legitimate 311 seconds, so the API canceled that node's
// re-probe before its own deadline every time and its inventory never landed.
func TestNormalizeProbeRequestTimeoutAdmitsALargeButRealBudget(t *testing.T) {
	nineDevices := CapabilityRequestTimeout(tonemap.BackendQSV,
		"/dev/dri/renderD128,/dev/dri/renderD129,/dev/dri/renderD130,/dev/dri/renderD131,"+
			"/dev/dri/renderD132,/dev/dri/renderD133,/dev/dri/renderD134,/dev/dri/renderD135,/dev/dri/renderD136")

	got := NormalizeProbeRequestTimeout(nineDevices.Milliseconds(), time.Minute)
	if got != nineDevices {
		t.Fatalf("normalized = %v, want the %v a nine-device node legitimately asks for", got, nineDevices)
	}

	// It is still a ceiling: the value comes off the wire from a worker, and a
	// caller holds a connection open for it.
	absurd := 24 * time.Hour
	if got := NormalizeProbeRequestTimeout(absurd.Milliseconds(), time.Minute); got != MaxCapabilityRequestTimeout() {
		t.Fatalf("normalized = %v for an absurd advertisement, want the %v ceiling", got, MaxCapabilityRequestTimeout())
	}
}

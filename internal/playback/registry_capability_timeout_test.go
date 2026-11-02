package playback

import (
	"os"
	"testing"
)

// The registry-only budget is advertised on a capability report and covered by
// its hash, so it must not be derived from the host. A proxy reports what its
// ffmpeg can do and walks no hardware; if this number tracked /dev/dri, a GPU
// appearing on a machine that also runs a proxy would move that proxy's
// capability hash — costing the API a refetch and a planning-cache drop to
// announce capabilities that did not change.
func TestRegistryCapabilityTimeoutIgnoresHostDevices(t *testing.T) {
	bare := RegistryCapabilityRequestTimeout()
	bareEndpoint := RegistryCapabilityEndpointTimeout()

	withRenderDevices(t)

	if got := RegistryCapabilityRequestTimeout(); got != bare {
		t.Fatalf("registry request budget = %s with render devices present, want the host-free %s", got, bare)
	}
	if got := RegistryCapabilityEndpointTimeout(); got != bareEndpoint {
		t.Fatalf("registry endpoint budget = %s with render devices present, want the host-free %s", got, bareEndpoint)
	}
	// The control: the hardware-aware budget does read the host, which is why
	// asking it for the software backend was not good enough.
	if CapabilityEndpointTimeout(HWAccelNone, "") == bareEndpoint {
		t.Fatal("CapabilityEndpointTimeout did not grow with the host's devices; " +
			"the fixture proves nothing about the registry budget's independence")
	}
}

// withRenderDevices points device discovery at a temp /dev/dri holding two
// classifiable render nodes, restored when the test ends.
func withRenderDevices(t *testing.T) {
	t.Helper()
	driDir := t.TempDir()
	sysDir := t.TempDir()
	for name, ids := range map[string][2]string{
		"renderD128": {"0x8086", "0x56a6"},
		"renderD129": {"0x10de", "0x2489"},
	} {
		if err := os.WriteFile(driDir+"/"+name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		devDir := sysDir + "/" + name + "/device"
		if err := os.MkdirAll(devDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(devDir+"/vendor", []byte(ids[0]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(devDir+"/device", []byte(ids[1]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	origDRI, origSys := defaultDRIDir, sysClassDRMDir
	defaultDRIDir, sysClassDRMDir = driDir, sysDir
	t.Cleanup(func() { defaultDRIDir, sysClassDRMDir = origDRI, origSys })
}

package playback

import (
	"os"
	"testing"
)

// fakeDeviceStat installs a stat function that reports only the given paths as
// present, restoring the real one on cleanup.
func fakeDeviceStat(t *testing.T, present ...string) {
	t.Helper()
	set := make(map[string]bool, len(present))
	for _, p := range present {
		set[p] = true
	}
	orig := hwDeviceStat
	hwDeviceStat = func(path string) error {
		if set[path] {
			return nil
		}
		return os.ErrNotExist
	}
	t.Cleanup(func() { hwDeviceStat = orig })
}

func resetDeviceLoad(t *testing.T) {
	t.Helper()
	hwDeviceLoad.mu.Lock()
	hwDeviceLoad.counts = map[string]int{}
	hwDeviceLoad.mu.Unlock()
}

func TestParseHWDevices(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"/dev/dri/renderD128", []string{"/dev/dri/renderD128"}},
		{"/dev/dri/renderD128,/dev/dri/renderD129", []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}},
		{" /dev/dri/renderD128 , /dev/dri/renderD129 ,", []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}},
	}
	for _, tc := range cases {
		got := ParseHWDevices(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("ParseHWDevices(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("ParseHWDevices(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

func TestResolveSessionHWDeviceSingleValuePassesThrough(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t) // nothing exists; single value must still pass through
	device, release := resolveSessionHWDevice("/dev/dri/renderD128", "qsv")
	defer release()
	if device != "/dev/dri/renderD128" {
		t.Fatalf("device = %q, want explicit single value unchanged", device)
	}
}

func TestResolveSessionHWDeviceBalancesAcrossList(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	configured := "/dev/dri/renderD128,/dev/dri/renderD129"

	dev1, release1 := resolveSessionHWDevice(configured, "qsv")
	if dev1 != "/dev/dri/renderD128" {
		t.Fatalf("first session device = %q, want first listed on tie", dev1)
	}
	dev2, release2 := resolveSessionHWDevice(configured, "qsv")
	if dev2 != "/dev/dri/renderD129" {
		t.Fatalf("second session device = %q, want least-loaded second device", dev2)
	}
	dev3, release3 := resolveSessionHWDevice(configured, "qsv")
	if dev3 != "/dev/dri/renderD128" {
		t.Fatalf("third session device = %q, want round-back to first on tie", dev3)
	}

	// Releasing the first session makes renderD128 least-loaded again.
	release1()
	release3()
	dev4, release4 := resolveSessionHWDevice(configured, "qsv")
	if dev4 != "/dev/dri/renderD128" {
		t.Fatalf("post-release device = %q, want freed first device", dev4)
	}
	release2()
	release4()
}

func TestResolveSessionHWDeviceSkipsMissingDevices(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD129")
	device, release := resolveSessionHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "qsv")
	defer release()
	if device != "/dev/dri/renderD129" {
		t.Fatalf("device = %q, want the only present device", device)
	}
}

func TestResolveSessionHWDeviceAllMissingFallsBackToFirst(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t) // none exist
	device, release := resolveSessionHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "qsv")
	defer release()
	if device != "/dev/dri/renderD128" {
		t.Fatalf("device = %q, want deterministic first entry when none exist", device)
	}
}

func TestResolveSessionHWDeviceSoftwareAccelDoesNotAcquire(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	configured := "/dev/dri/renderD128,/dev/dri/renderD129"

	_, releaseNone := resolveSessionHWDevice(configured, "none")
	defer releaseNone()

	// A software session must not shift the balance: the next GPU session
	// still lands on the first device.
	device, release := resolveSessionHWDevice(configured, "qsv")
	defer release()
	if device != "/dev/dri/renderD128" {
		t.Fatalf("device = %q, want first device unaffected by software session", device)
	}
}

func TestResolveSessionHWDeviceReleaseIsIdempotent(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	configured := "/dev/dri/renderD128,/dev/dri/renderD129"

	_, release1 := resolveSessionHWDevice(configured, "qsv")
	release1()
	release1() // double release must not underflow the count

	dev, release2 := resolveSessionHWDevice(configured, "qsv")
	defer release2()
	if dev != "/dev/dri/renderD128" {
		t.Fatalf("device = %q, want first device after idempotent release", dev)
	}
	hwDeviceLoad.mu.Lock()
	defer hwDeviceLoad.mu.Unlock()
	for device, count := range hwDeviceLoad.counts {
		if count < 0 {
			t.Fatalf("device %s count = %d, want never negative", device, count)
		}
	}
}

func TestPickRenderDeviceListAware(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")

	// A list picks a concrete device without acquiring load.
	dev := PickRenderDevice("/dev/dri/renderD128,/dev/dri/renderD129")
	if dev != "/dev/dri/renderD128" {
		t.Fatalf("PickRenderDevice(list) = %q, want least-loaded first device", dev)
	}

	// With the first device loaded, the list resolves to the second.
	_, release := resolveSessionHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "qsv")
	defer release()
	dev = PickRenderDevice("/dev/dri/renderD128,/dev/dri/renderD129")
	if dev != "/dev/dri/renderD129" {
		t.Fatalf("PickRenderDevice(list) = %q, want second device while first is loaded", dev)
	}

	// Single explicit value keeps the historical pass-through contract.
	if got := PickRenderDevice("/dev/dri/renderD42"); got != "/dev/dri/renderD42" {
		t.Fatalf("PickRenderDevice(single) = %q, want unchanged explicit value", got)
	}
}

func TestDetectHWAccelRenderDeviceDetails(t *testing.T) {
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

	info := DetectHWAccel()
	if len(info.RenderDeviceDetails) != 2 {
		t.Fatalf("RenderDeviceDetails len = %d, want 2: %+v", len(info.RenderDeviceDetails), info.RenderDeviceDetails)
	}
	first, second := info.RenderDeviceDetails[0], info.RenderDeviceDetails[1]
	if first.Path != driDir+"/renderD128" || first.Description != "Intel GPU (0x56a6)" {
		t.Fatalf("first device = %+v, want Intel description", first)
	}
	if second.Path != driDir+"/renderD129" || second.Description != "NVIDIA GPU (0x2489)" {
		t.Fatalf("second device = %+v, want NVIDIA description", second)
	}
}

func TestDescribeRenderDeviceUnknownVendor(t *testing.T) {
	sysDir := t.TempDir()
	devDir := sysDir + "/renderD130/device"
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devDir+"/vendor", []byte("0x1002\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origSys := sysClassDRMDir
	sysClassDRMDir = sysDir
	t.Cleanup(func() { sysClassDRMDir = origSys })

	if got := describeRenderDevice("/dev/dri/renderD130"); got != "AMD GPU" {
		t.Fatalf("describeRenderDevice() = %q, want AMD GPU without device id", got)
	}
	if got := describeRenderDevice("/dev/dri/renderD999"); got != "GPU" {
		t.Fatalf("describeRenderDevice() = %q, want bare GPU for unreadable sysfs", got)
	}
}

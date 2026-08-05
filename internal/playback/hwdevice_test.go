package playback

import (
	"os"
	"sync"
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

func TestParseHWDeviceSet(t *testing.T) {
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
		got := ParseHWDeviceSet(tc.in)
		if got.Empty() != (len(tc.want) == 0) || got.Multi() != (len(tc.want) > 1) {
			t.Fatalf("ParseHWDeviceSet(%q) Empty/Multi mismatch for %v", tc.in, tc.want)
		}
		list := got.List()
		if len(list) != len(tc.want) {
			t.Fatalf("ParseHWDeviceSet(%q).List() = %v, want %v", tc.in, list, tc.want)
		}
		for i := range list {
			if list[i] != tc.want[i] {
				t.Fatalf("ParseHWDeviceSet(%q).List() = %v, want %v", tc.in, list, tc.want)
			}
		}
	}
}

func TestAcquireHWDeviceEmptyValueStaysEmpty(t *testing.T) {
	resetDeviceLoad(t)
	device, release := AcquireHWDevice("", "qsv")
	defer release()
	if device != "" {
		t.Fatalf("device = %q, want empty so auto-detection applies", device)
	}
}

func TestAcquireHWDeviceSingleValuePassesThrough(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t) // nothing exists; single value must still pass through
	for _, accel := range []string{"qsv", "vaapi", "nvenc", "none"} {
		device, release := AcquireHWDevice("/dev/dri/renderD128", accel)
		if device != "/dev/dri/renderD128" {
			t.Fatalf("accel %s: device = %q, want explicit single value unchanged", accel, device)
		}
		release()
	}
}

func TestAcquireHWDeviceBalancesAcrossList(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	configured := "/dev/dri/renderD128,/dev/dri/renderD129"

	dev1, release1 := AcquireHWDevice(configured, "qsv")
	if dev1 != "/dev/dri/renderD128" {
		t.Fatalf("first workload device = %q, want first listed on tie", dev1)
	}
	dev2, release2 := AcquireHWDevice(configured, "vaapi")
	if dev2 != "/dev/dri/renderD129" {
		t.Fatalf("second workload device = %q, want least-loaded second device", dev2)
	}
	dev3, release3 := AcquireHWDevice(configured, "qsv")
	if dev3 != "/dev/dri/renderD128" {
		t.Fatalf("third workload device = %q, want round-back to first on tie", dev3)
	}

	// Releasing the first workload makes renderD128 least-loaded again.
	release1()
	release3()
	dev4, release4 := AcquireHWDevice(configured, "qsv")
	if dev4 != "/dev/dri/renderD128" {
		t.Fatalf("post-release device = %q, want freed first device", dev4)
	}
	release2()
	release4()
}

func TestAcquireHWDeviceSkipsMissingDevices(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD129")
	device, release := AcquireHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "qsv")
	defer release()
	if device != "/dev/dri/renderD129" {
		t.Fatalf("device = %q, want the only present device", device)
	}
}

func TestAcquireHWDeviceAllMissingFallsBackToFirst(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t) // none exist
	device, release := AcquireHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "qsv")
	defer release()
	if device != "/dev/dri/renderD128" {
		t.Fatalf("device = %q, want deterministic first entry when none exist", device)
	}
}

func TestAcquireHWDeviceNVENCMultiListUsesFirstWithoutReserving(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t) // NVENC entries are CUDA indexes/UUIDs, never present as paths
	device, release := AcquireHWDevice("0,1", "nvenc")
	defer release()
	if device != "0" {
		t.Fatalf("device = %q, want first NVENC entry", device)
	}
	if got := hwDeviceActiveCount("0"); got != 0 {
		t.Fatalf("active count = %d, want no reservation for NVENC", got)
	}
}

func TestAcquireHWDeviceSoftwareAccelDoesNotReserve(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	configured := "/dev/dri/renderD128,/dev/dri/renderD129"

	_, releaseNone := AcquireHWDevice(configured, "none")
	defer releaseNone()

	// A software workload must not shift the balance: the next GPU workload
	// still lands on the first device.
	device, release := AcquireHWDevice(configured, "qsv")
	defer release()
	if device != "/dev/dri/renderD128" {
		t.Fatalf("device = %q, want first device unaffected by software workload", device)
	}
}

func TestAcquireHWDeviceReleaseIsIdempotent(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	configured := "/dev/dri/renderD128,/dev/dri/renderD129"

	_, release1 := AcquireHWDevice(configured, "qsv")
	release1()
	release1() // double release must not underflow the count

	dev, release2 := AcquireHWDevice(configured, "qsv")
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

func TestPickRenderDeviceExplicitValuePassesThrough(t *testing.T) {
	// PickRenderDevice is auto-detection only; list resolution happens in
	// AcquireHWDevice before args are built, so an explicit value — even a
	// stale CSV — passes through untouched.
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

func TestAcquireHWDeviceConcurrentStartsBalanceExactly(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	configured := "/dev/dri/renderD128,/dev/dri/renderD129"

	const workloads = 8
	var wg sync.WaitGroup
	devices := make([]string, workloads)
	releases := make([]func(), workloads)
	for i := range workloads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			devices[i], releases[i] = AcquireHWDevice(configured, "qsv")
		}()
	}
	wg.Wait()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	counts := map[string]int{}
	for _, device := range devices {
		counts[device]++
	}
	// Atomic select+reserve guarantees an exact split; a two-step selection
	// could pile concurrent starts onto one device.
	if counts["/dev/dri/renderD128"] != workloads/2 || counts["/dev/dri/renderD129"] != workloads/2 {
		t.Fatalf("concurrent workload split = %v, want exact %d/%d", counts, workloads/2, workloads/2)
	}
}

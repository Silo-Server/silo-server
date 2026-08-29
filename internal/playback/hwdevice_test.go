package playback

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
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

// An empty setting resolves to nothing only when there is nothing to resolve to.
// Pointed at an empty device directory for the same reason as the counting test
// below: with a real /dev/dri present this now returns the device execution
// would pick, which is the point of resolving it here.
func TestAcquireHWDeviceEmptyValueStaysEmptyWithoutDevices(t *testing.T) {
	resetDeviceLoad(t)
	original := defaultDRIDir
	defaultDRIDir = t.TempDir()
	t.Cleanup(func() { defaultDRIDir = original })

	device, release := AcquireHWDevice("", "qsv")
	defer release()
	if device != "" {
		t.Fatalf("device = %q, want empty so auto-detection applies downstream", device)
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

// The single-GPU node is the common deployment. Having nothing to balance is no
// reason to leave its workloads uncounted: sessions would read 0 on a node
// whose engine busy percentage says it is transcoding.
func TestAcquireHWDeviceSingleRenderDeviceIsCounted(t *testing.T) {
	for _, accel := range []string{"qsv", "vaapi"} {
		t.Run(accel, func(t *testing.T) {
			resetDeviceLoad(t)
			fakeDeviceStat(t) // the device need not exist for the count to be right
			_, releaseFirst := AcquireHWDevice("/dev/dri/renderD128", accel)
			_, releaseSecond := AcquireHWDevice("/dev/dri/renderD128", accel)

			if got := HWDeviceLoadSnapshot()["/dev/dri/renderD128"]; got != 2 {
				t.Fatalf("snapshot count = %d, want both workloads counted (%v)", got, HWDeviceLoadSnapshot())
			}
			releaseFirst()
			releaseSecond()
			if got := HWDeviceLoadSnapshot(); len(got) != 0 {
				t.Fatalf("snapshot after release = %v, want empty", got)
			}
		})
	}
}

// With no render device to name, the workload stays uncounted rather than being
// attributed to a device that does not exist. The device directory is pointed at
// an empty temp dir rather than left at the real /dev/dri: unconfigured
// acquisition now falls back to the device execution would pick, so on a host
// that actually has a GPU this would otherwise assert the opposite of the truth.
func TestAcquireHWDeviceUnconfiguredRenderDeviceIsNotCounted(t *testing.T) {
	resetDeviceLoad(t)
	original := defaultDRIDir
	defaultDRIDir = t.TempDir()
	t.Cleanup(func() { defaultDRIDir = original })

	_, release := AcquireHWDevice("", "vaapi")
	defer release()
	if got := HWDeviceLoadSnapshot(); len(got) != 0 {
		t.Fatalf("snapshot = %v, want no count for an unresolved device", got)
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

// NVENC is counted but never balanced: a multi-entry list still resolves to its
// first entry, because CUDA indexes are not render-node paths and the balancer
// has no way to compare them.
func TestAcquireHWDeviceNVENCMultiListUsesFirstWithoutBalancing(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t) // NVENC entries are CUDA indexes/UUIDs, never present as paths
	first, releaseFirst := AcquireHWDevice("0,1", "nvenc")
	defer releaseFirst()
	second, releaseSecond := AcquireHWDevice("0,1", "nvenc")
	defer releaseSecond()

	if first != "0" || second != "0" {
		t.Fatalf("devices = %q, %q; want both on the first NVENC entry", first, second)
	}
	// ffmpeg is handed the bare CUDA index, but the count is keyed the way
	// resource sampling names that GPU — otherwise the join drops it.
	if got := hwDeviceActiveCount("cuda:0"); got != 2 {
		t.Fatalf("active count = %d, want both NVENC workloads counted under cuda:0", got)
	}
}

// NVENC selects GPUs by CUDA index, and the sampler publishes them as "cuda:N".
// Counting the raw configured value would report zero sessions on every
// explicitly-selected NVIDIA GPU while it transcodes.
func TestNVENCAccountingDeviceUsesSamplerNamespace(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{
		{"", DefaultNVENCDevice},
		{"0", "cuda:0"},
		{"1", "cuda:1"},
		{"1,0", "cuda:1"},
		{"cuda:1", "cuda:1"},
		{"GPU-1234abcd", "GPU-1234abcd"},
		{"/dev/dri/renderD128", "/dev/dri/renderD128"},
	} {
		if got := nvencAccountingDevice(tc.configured); got != tc.want {
			t.Fatalf("nvencAccountingDevice(%q) = %q, want %q", tc.configured, got, tc.want)
		}
	}
}

// An unconfigured NVENC workload lands on the CUDA default, which is what
// ffmpeg will actually use, so per-device reporting is not blank on the most
// common NVIDIA deployment.
func TestAcquireHWDeviceNVENCUnconfiguredCountsCUDADefault(t *testing.T) {
	resetDeviceLoad(t)
	device, release := AcquireHWDevice("", "nvenc")
	if device != "" {
		t.Fatalf("device = %q, want empty so auto-detection applies", device)
	}
	if got := hwDeviceActiveCount(DefaultNVENCDevice); got != 1 {
		t.Fatalf("active count = %d, want the workload counted against %s", got, DefaultNVENCDevice)
	}
	release()
	if got := hwDeviceActiveCount(DefaultNVENCDevice); got != 0 {
		t.Fatalf("active count after release = %d, want 0", got)
	}
}

// The snapshot is what node metrics report per device; it must show live
// workloads and drop devices back out once they are released.
func TestHWDeviceLoadSnapshot(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	_, releaseA := AcquireHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "qsv")
	_, releaseB := AcquireHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "qsv")
	_, releaseNVENC := AcquireHWDevice("", "nvenc")

	snapshot := HWDeviceLoadSnapshot()
	want := map[string]int{
		"/dev/dri/renderD128": 1,
		"/dev/dri/renderD129": 1,
		DefaultNVENCDevice:    1,
	}
	if len(snapshot) != len(want) {
		t.Fatalf("snapshot = %v, want %v", snapshot, want)
	}
	for device, count := range want {
		if snapshot[device] != count {
			t.Fatalf("snapshot[%q] = %d, want %d (snapshot %v)", device, snapshot[device], count, snapshot)
		}
	}

	// The copy must not track later changes, or a reader would see counts move
	// underneath a snapshot it already published.
	releaseA()
	releaseB()
	releaseNVENC()
	if snapshot["/dev/dri/renderD128"] != 1 {
		t.Fatal("snapshot changed after release; it is not a copy")
	}
	if remaining := HWDeviceLoadSnapshot(); len(remaining) != 0 {
		t.Fatalf("snapshot after all releases = %v, want empty", remaining)
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

func TestAcquireHWDeviceAvoidsFailedRenderDeviceAndReservesAlternate(t *testing.T) {
	resetDeviceLoad(t)
	fakeDeviceStat(t, "/dev/dri/renderD128", "/dev/dri/renderD129")
	configured := "/dev/dri/renderD128,/dev/dri/renderD129"
	got, workload, release := acquireHWDevice(configured, "qsv", "/dev/dri/renderD128")
	defer release()
	if got != "/dev/dri/renderD129" {
		t.Fatalf("alternate device = %q, want renderD129", got)
	}
	if workload != got {
		t.Fatalf("workload device = %q, want the selected device %q", workload, got)
	}
	if active := hwDeviceActiveCount(got); active != 1 {
		t.Fatalf("alternate device active count = %d, want 1", active)
	}
	if got, _, releaseNVENC := acquireHWDevice(configured, "nvenc", "/dev/dri/renderD128"); got != "/dev/dri/renderD128" {
		releaseNVENC()
		t.Fatalf("NVENC retry device = %q, want first configured device", got)
	} else {
		releaseNVENC()
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

// The probe matrix stops at a ceiling, so past it there is no verdict to
// dispatch on. Balancing over the full configured list would hand a share of the
// node's transcodes to a device the walk never reached, and the failure would
// land after the session started — while the published capabilities say nothing
// about that device either way.
func TestAcquireHWDeviceNeverSelectsPastTheProbeCeiling(t *testing.T) {
	resetDeviceLoad(t)
	devices := make([]string, 0, tonemap.MaxProbedDevices+1)
	for i := range tonemap.MaxProbedDevices + 1 {
		devices = append(devices, "/dev/dri/renderD"+strconv.Itoa(128+i))
	}
	fakeDeviceStat(t, devices...)
	beyond := devices[tonemap.MaxProbedDevices]
	configured := strings.Join(devices, ",")

	// Every probed device has to be handed a workload before the one past the
	// ceiling could come up, so run enough starts to cover the whole list twice.
	var releases []func()
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
	})
	for range len(devices) * 2 {
		selected, release := AcquireHWDevice(configured, "qsv")
		releases = append(releases, release)
		if selected == beyond {
			t.Fatalf("workload dispatched to %q, which is past the %d-device probe ceiling",
				beyond, tonemap.MaxProbedDevices)
		}
	}
}

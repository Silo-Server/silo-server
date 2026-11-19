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

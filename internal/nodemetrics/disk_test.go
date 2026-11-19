package nodemetrics

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// diskFixture wires a sampler whose statfs is fully controlled by the test and
// whose probe completions are observable, so nothing here waits on a sleep.
type diskFixture struct {
	sampler *Sampler
	clock   *fakeClock
	// mu guards answers and block, which probe goroutines read.
	mu sync.Mutex
	// answers maps a path to the result its probe returns.
	answers map[string]fsStats
	// block, when set for a path, parks that probe until it is closed —
	// standing in for statfs(2) on a dead NFS server.
	block map[string]chan struct{}
	done  chan string
}

// wedge makes every subsequent probe of path park forever, as a dead NFS server
// does. Call it only when no probe of that path is in flight, or the test is
// asserting on which sample started the parked probe.
func (f *diskFixture) wedge(t *testing.T, path string) {
	t.Helper()
	gate := make(chan struct{})
	f.mu.Lock()
	f.block[path] = gate
	f.mu.Unlock()
	t.Cleanup(func() { close(gate) })
}

func (f *diskFixture) answer(path string, stats fsStats) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[path] = stats
}

func newDiskFixture(t *testing.T, paths ...string) *diskFixture {
	t.Helper()
	tree := newProcTree(t)
	tree.writeIdleHost()
	clock := newFakeClock()

	f := &diskFixture{
		clock:   clock,
		answers: map[string]fsStats{},
		block:   map[string]chan struct{}{},
		done:    make(chan string, 64),
	}
	roots := paths
	scratch := ""
	if len(roots) > 0 {
		scratch, roots = roots[0], roots[1:]
	}
	f.sampler = newTestSampler(t, tree, clock, Options{
		ScratchDir: staticScratch(scratch),
		MediaRoots: func(context.Context) []string { return roots },
	})
	f.sampler.diskProbeDone = f.done
	f.sampler.statfs = func(path string) (fsStats, error) {
		f.mu.Lock()
		gate, blocked := f.block[path]
		stats, known := f.answers[path]
		f.mu.Unlock()
		if blocked {
			<-gate
		}
		if !known {
			return fsStats{}, os.ErrNotExist
		}
		return stats, nil
	}
	return f
}

// sampleAndSettle runs one sampling pass and waits for the probes it started,
// so the next pass begins with nothing in flight. Without this, whether a probe
// launched by the previous pass is still running is a race, and every assertion
// about in-flight state becomes timing-dependent.
func (f *diskFixture) sampleAndSettle(t *testing.T, probes int) []DiskStats {
	t.Helper()
	disks := f.disks(t)
	f.awaitProbes(t, probes)
	return disks
}

// awaitProbes blocks until n probe completions have been reported. Waiting on
// the sampler's own signal keeps the test deterministic without a sleep.
func (f *diskFixture) awaitProbes(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-f.done:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a disk probe to complete")
		}
	}
}

func (f *diskFixture) disks(t *testing.T) []DiskStats {
	t.Helper()
	f.sampler.sample(context.Background())
	system := f.sampler.Snapshot().System
	if system == nil {
		t.Fatal("no system stats in snapshot")
	}
	return system.Disks
}

// The contract that matters most: a mount whose server died reports its last
// good numbers marked stale, and never delays a sample.
func TestDiskStatsReportsHungMountAsStaleWithoutBlocking(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/nfs")
	f.answer("/transcode", fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "a:1"})
	f.answer("/nfs", fsStats{UsedBytes: 900 << 30, TotalBytes: 1000 << 30, FSID: "n:1"})

	f.sampleAndSettle(t, 2)
	if disks := f.sampleAndSettle(t, 2); disks[1].Stale {
		t.Fatalf("disks[1] = %+v, want fresh before the mount wedges", disks[1])
	}

	// The NFS server goes away: every subsequent probe parks forever. Nothing is
	// in flight here, so the next sample is unambiguously the one that parks.
	f.wedge(t, "/nfs")

	// Well past the probe budget and one sampling interval.
	f.clock.advance(time.Minute)
	wedgedAt := f.clock.at
	disks := f.disks(t)

	f.awaitProbes(t, 1) // the scratch probe still completes normally
	if len(disks) != 2 {
		t.Fatalf("disks = %+v, want both mounts reported", disks)
	}
	if disks[1].Path != "/nfs" || !disks[1].Stale {
		t.Fatalf("disks[1] = %+v, want /nfs marked stale", disks[1])
	}
	if disks[1].UsedGB != 900 || disks[1].TotalGB != 1000 {
		t.Fatalf("disks[1] = %+v, want the last good numbers preserved", disks[1])
	}

	// A permanently stuck mount must not accumulate a goroutine per sample.
	for range 5 {
		f.clock.advance(time.Minute)
		f.sampleAndSettle(t, 1) // only the scratch probe can complete
	}
	f.sampler.diskMu.Lock()
	entry := f.sampler.disks["/nfs"]
	inFlight := entry.inFlight
	startedAt := entry.startedAt
	f.sampler.diskMu.Unlock()
	if !inFlight {
		t.Fatal("stuck mount is not marked in flight")
	}
	// startedAt still points at the sample that launched the parked probe, which
	// is only true if no later sample launched another one.
	if !startedAt.Equal(wedgedAt) {
		t.Fatalf("stuck mount was re-probed: startedAt = %v, want %v", startedAt, wedgedAt)
	}
}

// Bounding the paths offered per sample is not enough on its own. A probe stuck
// on a dead mount is deliberately kept, so a deployment whose library roots
// churn while mounts are wedged would retire one set of parked goroutines'
// paths and immediately be free to park a fresh set for the replacements.
// Without a ceiling the parked count grows every time that repeats.
func TestDiskProbesAreBoundedAcrossReconfiguration(t *testing.T) {
	f := newDiskFixture(t, "/transcode")
	// Every mount wedges: the probe goroutine parks and never returns, which is
	// what statfs on a dead network mount actually does.
	roots := make([]string, 0, maxOutstandingDiskProbes*3)
	for i := range cap(roots) {
		path := "/media/wedged-" + strconv.Itoa(i)
		roots = append(roots, path)
		f.wedge(t, path)
	}
	f.wedge(t, "/transcode")

	// Offer a fresh set of roots each pass, as a library reconfiguration would.
	for pass := range 3 {
		window := roots[pass*maxOutstandingDiskProbes : (pass+1)*maxOutstandingDiskProbes]
		f.sampler.refreshDisks(append([]string{"/transcode"}, window...), f.clock.now())
	}

	f.sampler.diskMu.Lock()
	outstanding := f.sampler.probesInFlight
	f.sampler.diskMu.Unlock()
	if outstanding > maxOutstandingDiskProbes {
		t.Fatalf("outstanding probes = %d, want at most %d", outstanding, maxOutstandingDiskProbes)
	}
}

// A probe parked on a mount that has since been retired holds one global probe
// slot until the process exits. Offering the same paths in the same order every
// sample then spends the remaining budget on the same prefix, and the path at
// the end of the list is never measured even once — reported unavailable for as
// long as the dead mount stays dead, which is a lie about a healthy disk.
func TestRefreshDisksRotatesProbesPastAWedgedRetiredMount(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	s := newTestSampler(t, tree, clock, Options{ScratchDir: staticScratch("/transcode")})
	done := make(chan string, 64)
	s.diskProbeDone = done

	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	var mu sync.Mutex
	probed := map[string]bool{}
	s.statfs = func(path string) (fsStats, error) {
		if path == "/dead" {
			<-gate
			return fsStats{}, os.ErrNotExist
		}
		mu.Lock()
		probed[path] = true
		mu.Unlock()
		return fsStats{UsedBytes: 1 << 30, TotalBytes: 2 << 30, FSID: path}, nil
	}

	now := clock.now()
	// One sample parks a probe on a mount that the next configuration drops.
	s.refreshDisks([]string{"/dead"}, now)

	live := []string{"/transcode"}
	for i := range maxOutstandingDiskProbes - 1 {
		live = append(live, "/media/"+strconv.Itoa(i))
	}
	// live fills the ceiling exactly and one slot is gone for good, so each pass
	// can start one fewer probe than there are paths.
	perPass := len(live) - 1
	for range len(live) {
		s.refreshDisks(live, now)
		for range perPass {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for a disk probe to complete")
			}
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range live {
		if !probed[path] {
			t.Fatalf("%s was never probed across %d passes; the retired mount starved it", path, len(live))
		}
	}
}

// Capacity is measured against what this process can write, not against the
// device: a filesystem that reserves blocks for root hands them to nobody else,
// and counting them as headroom is what makes a volume with nothing left read
// as 95% full — exactly where the scratch admission guard sits.
func TestFSCapacityExcludesBlocksReservedFromThisProcess(t *testing.T) {
	const block = 4096
	// 100 GiB with a 5% root reserve, filled to the point an unprivileged
	// process can write nothing more.
	total := uint64(100<<30) / block
	reserved := total / 20
	stats := fsCapacity(total, reserved, 0, block)

	if stats.UsedBytes != (total-reserved)*block {
		t.Fatalf("UsedBytes = %d, want the reserve counted as used, as df does", stats.UsedBytes)
	}
	if stats.TotalBytes != stats.UsedBytes {
		t.Fatalf("TotalBytes = %d, want %d: no writable bytes remain", stats.TotalBytes, stats.UsedBytes)
	}

	// An empty volume reports the whole writable capacity, which is the device
	// size less the reserve rather than the device size.
	empty := fsCapacity(total, total, total-reserved, block)
	if empty.UsedBytes != 0 {
		t.Fatalf("UsedBytes = %d on an empty volume, want 0", empty.UsedBytes)
	}
	if empty.TotalBytes != (total-reserved)*block {
		t.Fatalf("TotalBytes = %d, want the reserve excluded from capacity", empty.TotalBytes)
	}

	// Nonsense counters must not wrap an unsigned subtraction into a petabyte.
	if got := fsCapacity(10, 20, 5, block); got.UsedBytes != 0 {
		t.Fatalf("UsedBytes = %d for free > total, want 0", got.UsedBytes)
	}
}

// staticScratch is the provider for a host whose scratch dir never moves, which
// is every test but the one covering a hot-reloaded one.
func staticScratch(path string) func() string {
	return func() string { return path }
}

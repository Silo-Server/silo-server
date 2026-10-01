package nodemetrics

import (
	"context"
	"errors"
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
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
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

// Scratch and a media root frequently live on one volume. Reporting both would
// double a dashboard's disk row and make one filling disk look like two.
func TestDiskStatsDeduplicatesByFilesystem(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/media", "/other")
	f.answer("/transcode", fsStats{UsedBytes: 100 << 30, TotalBytes: 500 << 30, FSID: "a:1"})
	f.answer("/media", fsStats{UsedBytes: 100 << 30, TotalBytes: 500 << 30, FSID: "a:1"})
	f.answer("/other", fsStats{UsedBytes: 7 << 30, TotalBytes: 8 << 30, FSID: "b:2"})

	f.sampleAndSettle(t, 3) // the first pass only launches the probes
	disks := f.sampleAndSettle(t, 3)

	if len(disks) != 2 {
		t.Fatalf("disks = %+v, want the two distinct filesystems", disks)
	}
	if disks[0].Path != "/transcode" {
		t.Fatalf("disks[0].Path = %q, want the scratch dir first", disks[0].Path)
	}
	if disks[0].UsedGB != 100 || disks[0].TotalGB != 500 {
		t.Fatalf("disks[0] = %+v, want 100/500 GB", disks[0])
	}
	if disks[1].Path != "/other" {
		t.Fatalf("disks[1].Path = %q, want the second filesystem", disks[1].Path)
	}
	for _, disk := range disks {
		if disk.Stale || disk.Unavailable {
			t.Fatalf("disk %+v flagged stale/unavailable on a fresh probe", disk)
		}
	}
}

// The API stores a node's sample opaquely and does not know its transcode
// directory, so the scratch entry has to identify itself: the admission guard
// and the Prometheus label both find it by this flag, not by matching a path.
func TestDiskStatsFlagsTheScratchMount(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/media", "/missing")
	f.answer("/transcode", fsStats{UsedBytes: 96 << 30, TotalBytes: 100 << 30, FSID: "a:1"})
	f.answer("/media", fsStats{UsedBytes: 10 << 30, TotalBytes: 100 << 30, FSID: "b:2"})

	f.sampleAndSettle(t, 3)
	disks := f.sampleAndSettle(t, 3)

	if len(disks) != 3 {
		t.Fatalf("disks = %+v, want three entries", disks)
	}
	if !disks[0].Scratch || disks[0].Path != "/transcode" {
		t.Fatalf("disks[0] = %+v, want the scratch dir flagged and first", disks[0])
	}
	for _, disk := range disks[1:] {
		if disk.Scratch {
			t.Fatalf("non-scratch mount flagged as scratch: %+v", disk)
		}
	}
}

// A scratch dir that does not exist yet is still the scratch entry: an
// unavailable reading has to be distinguishable from a media root's, and the
// admission guard depends on telling "cannot measure" from "not the scratch".
func TestDiskStatsFlagsAnUnavailableScratchMount(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/media")
	f.answer("/media", fsStats{UsedBytes: 10 << 30, TotalBytes: 100 << 30, FSID: "b:2"})

	f.sampleAndSettle(t, 2)
	disks := f.sampleAndSettle(t, 2)

	if len(disks) == 0 || disks[0].Path != "/transcode" {
		t.Fatalf("disks = %+v, want the scratch dir first", disks)
	}
	if !disks[0].Unavailable || !disks[0].Scratch {
		t.Fatalf("disks[0] = %+v, want an unavailable scratch entry", disks[0])
	}
}

// A sampler with no scratch dir — a proxy node — flags nothing, so a reader
// never mistakes a media root for the transcode volume.
func TestDiskStatsFlagsNoScratchWithoutAScratchDir(t *testing.T) {
	f := newDiskFixture(t, "", "/media")
	f.answer("/media", fsStats{UsedBytes: 10 << 30, TotalBytes: 100 << 30, FSID: "b:2"})

	f.sampleAndSettle(t, 1)
	disks := f.sampleAndSettle(t, 1)

	for _, disk := range disks {
		if disk.Scratch {
			t.Fatalf("mount flagged as scratch with no scratch dir configured: %+v", disk)
		}
	}
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

// A media root that exists on other nodes but not this one is reported rather
// than hidden: an operator needs to see the gap, and zeros would read as an
// empty disk.
func TestDiskStatsReportsUnseenPathAsUnavailable(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/media-on-another-node")
	f.answer("/transcode", fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "a:1"})

	f.sampleAndSettle(t, 2)
	disks := f.sampleAndSettle(t, 2)

	if len(disks) != 2 {
		t.Fatalf("disks = %+v, want both paths reported", disks)
	}
	if !disks[1].Unavailable {
		t.Fatalf("disks[1] = %+v, want Unavailable", disks[1])
	}
	if disks[1].UsedGB != 0 || disks[1].TotalGB != 0 {
		t.Fatalf("disks[1] = %+v, want no capacity numbers alongside Unavailable", disks[1])
	}
}

// A deployment with dozens of library roots must not grow the health response
// without bound. Only the capped set is probed, so that is also how many probe
// completions this can wait for.
func TestDiskStatsCapsMountCount(t *testing.T) {
	paths := make([]string, 0, 12)
	for i := range 12 {
		paths = append(paths, "/mount"+itoa(i))
	}
	f := newDiskFixture(t, paths...)
	for i, path := range paths {
		f.answer(path, fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "fs:" + itoa(i)})
	}

	f.sampleAndSettle(t, maxSampledDisks)
	disks := f.sampleAndSettle(t, maxSampledDisks)

	if len(disks) != maxSampledDisks {
		t.Fatalf("len(disks) = %d, want the cap of %d", len(disks), maxSampledDisks)
	}
}

// The cap exists so a health response does not grow with the library count, and
// a path this host cannot measure costs an entry just like a measured one. An
// API host whose library roots live on the nodes reports nothing but
// unavailable entries.
func TestDiskStatsCapsUnavailableMountsToo(t *testing.T) {
	paths := make([]string, 0, 20)
	for i := range 20 {
		paths = append(paths, "/mount"+itoa(i))
	}
	f := newDiskFixture(t, paths...)
	// Only the scratch dir can be measured; every library root fails statfs.
	f.answer(paths[0], fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "a:1"})

	f.sampleAndSettle(t, maxSampledDisks)
	disks := f.sampleAndSettle(t, maxSampledDisks)

	if len(disks) != maxSampledDisks {
		t.Fatalf("len(disks) = %d, want the cap of %d even when the mounts are unavailable", len(disks), maxSampledDisks)
	}
}

// Several filesystems leave statfs's f_fsid zero — FUSE has no fsid in its
// protocol at all, which covers rclone, mergerfs and s3fs. Treating that as an
// identity collapses unrelated media roots onto one entry, and the mount that
// disappears is a real volume with real capacity nobody is watching any more.
func TestDiskStatsDoesNotDeduplicateMountsWithoutAnFSID(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/mnt/rclone-movies", "/mnt/rclone-tv")
	f.answer("/transcode", fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "a:1"})
	// What osStatfs reports for a FUSE mount: no usable filesystem id.
	f.answer("/mnt/rclone-movies", fsStats{UsedBytes: 1 << 30, TotalBytes: 100 << 30})
	f.answer("/mnt/rclone-tv", fsStats{UsedBytes: 98 << 30, TotalBytes: 100 << 30})

	f.sampleAndSettle(t, 3)
	disks := f.sampleAndSettle(t, 3)

	if len(disks) != 3 {
		t.Fatalf("disks = %+v, want all three mounts reported", disks)
	}
	if disks[2].Path != "/mnt/rclone-tv" || disks[2].UsedGB != 98 {
		t.Fatalf("disks[2] = %+v, want the nearly full second FUSE mount kept", disks[2])
	}
}

// The dedup itself must still work for filesystems that do publish an id.
func TestFormatFSIDDropsAZeroIdentity(t *testing.T) {
	if got := formatFSID(0, 0); got != "" {
		t.Fatalf("formatFSID(0, 0) = %q, want no identity", got)
	}
	if got := formatFSID(0x1a, 0x2b); got != "1a:2b" {
		t.Fatalf("formatFSID(0x1a, 0x2b) = %q, want 1a:2b", got)
	}
}

// Library roots change over a server's life. Entries for paths no longer
// configured must be forgotten rather than accumulate for months.
func TestDiskStatsForgetsPathsNoLongerConfigured(t *testing.T) {
	roots := []string{"/media-a"}
	tree := newProcTree(t)
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	clock := newFakeClock()
	done := make(chan string, 16)

	s := newTestSampler(t, tree, clock, Options{
		ScratchDir: staticScratch("/transcode"),
		MediaRoots: func(context.Context) []string { return roots },
	})
	s.diskProbeDone = done
	s.statfs = func(path string) (fsStats, error) {
		return fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: path}, nil
	}

	s.sample(context.Background())
	for range 2 {
		<-done
	}
	if disks := s.Snapshot().System.Disks; len(disks) != 2 {
		t.Fatalf("disks = %+v, want both paths tracked", disks)
	}

	// The library moves to a different root.
	roots = []string{"/media-b"}
	s.sample(context.Background())
	for range 2 {
		<-done
	}
	s.sample(context.Background())

	s.diskMu.Lock()
	_, stillTracked := s.disks["/media-a"]
	order := append([]string(nil), s.diskOrder...)
	s.diskMu.Unlock()
	if stillTracked {
		t.Fatalf("the removed root is still tracked: %v", order)
	}
	if len(order) != 2 {
		t.Fatalf("diskOrder = %v, want only the two current paths", order)
	}
}

func TestOSStatfsOnUnsupportedPlatformIsNotFatal(t *testing.T) {
	// Guards the non-Linux build path: the fallback must return an error rather
	// than panic, since NewSampler installs it unconditionally.
	if _, err := osStatfs("/definitely/not/a/path"); err == nil {
		t.Fatal("statfs on a missing path returned no error")
	} else if errors.Is(err, errors.ErrUnsupported) {
		t.Log("platform has no statfs; sampler correctly reports paths unavailable")
	}
}

// A mount's role is what the unauthenticated surfaces name it by, so it has to
// be assigned to the mount rather than to its luck this pass: numbering only the
// measurable entries would slide every library root up a place the moment one
// went unavailable.
func TestDiskStatsAssignsPositionalRoles(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/media/movies", "/media/shows")
	f.answer("/transcode", fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "a:1"})
	// /media/movies is never answered, so it stays unavailable.
	f.answer("/media/shows", fsStats{UsedBytes: 3 << 30, TotalBytes: 30 << 30, FSID: "c:1"})

	f.sampleAndSettle(t, 3)
	disks := f.sampleAndSettle(t, 3)

	got := map[string]string{}
	for _, disk := range disks {
		got[disk.Path] = disk.Role
	}
	want := map[string]string{
		"/transcode":    ScratchDiskRole,
		"/media/movies": "library-1",
		"/media/shows":  "library-2",
	}
	for path, role := range want {
		if got[path] != role {
			t.Fatalf("role for %s = %q, want %q (all: %v)", path, got[path], role, got)
		}
	}
}

// statfs on a dead network mount is uninterruptible, so every probe started is a
// goroutine that may never return. Bounding only the published output would let
// a deployment with forty library roots start forty probes every interval to
// fill eight slots.
func TestDiskPathsAreBoundedByTheSampleCap(t *testing.T) {
	roots := make([]string, 0, maxSampledDisks+4)
	for i := range maxSampledDisks + 4 {
		roots = append(roots, "/media/root-"+strconv.Itoa(i))
	}
	f := newDiskFixture(t, append([]string{"/transcode"}, roots...)...)

	paths := f.sampler.diskPaths(context.Background())
	if len(paths) != maxSampledDisks {
		t.Fatalf("probed paths = %d, want the cap of %d", len(paths), maxSampledDisks)
	}
	// The scratch dir is what admission control reads, so it is never the entry
	// the cap drops.
	if paths[0] != "/transcode" {
		t.Fatalf("first probed path = %q, want the scratch dir", paths[0])
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

// Scratch keeps its priority through the rotation: it is the mount transcode
// admission reads, so it must be the one that gets a freed slot.
func TestRefreshDisksAlwaysOffersScratchFirst(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	s := newTestSampler(t, tree, clock, Options{ScratchDir: staticScratch("/transcode")})
	// Which mount is the scratch one is resolved once per pass, so the ordering
	// below is asking a question a pass has to have asked first.
	s.diskPaths(context.Background())

	candidates := []*diskEntry{{path: "/transcode"}, {path: "/a"}, {path: "/b"}, {path: "/c"}}
	for pass := range 6 {
		ordered := s.probeOrderLocked(candidates)
		if ordered[0].path != "/transcode" {
			t.Fatalf("pass %d offered %q first, want the scratch dir", pass, ordered[0].path)
		}
		if len(ordered) != len(candidates) {
			t.Fatalf("pass %d offered %d entries, want all %d", pass, len(ordered), len(candidates))
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

// playback.transcode_dir is hot-reloadable, and in integrated mode this host is
// the one transcoding. A sampler that captured the directory at startup would
// keep reporting headroom on a volume nothing writes to while the new one fills
// unwatched — and the scratch role is what transcode admission reads.
func TestScratchDirFollowsAHotReloadedTranscodeDir(t *testing.T) {
	f := newDiskFixture(t, "/transcode-old")
	f.answer("/transcode-old", fsStats{UsedBytes: 10 << 30, TotalBytes: 500 << 30, FSID: "a:1"})
	f.answer("/transcode-new", fsStats{UsedBytes: 490 << 30, TotalBytes: 500 << 30, FSID: "b:2"})
	current := "/transcode-old"
	f.sampler.scratchDirFn = func() string { return current }

	f.sampleAndSettle(t, 1)
	f.sampleAndSettle(t, 1)
	scratch := scratchDisk(t, f.sampler)
	if scratch.Path != "/transcode-old" {
		t.Fatalf("scratch = %q, want the configured /transcode-old", scratch.Path)
	}

	// An operator repoints the setting at a nearly full volume.
	current = "/transcode-new"
	f.sampleAndSettle(t, 1)
	f.sampleAndSettle(t, 1)
	scratch = scratchDisk(t, f.sampler)
	if scratch.Path != "/transcode-new" {
		t.Fatalf("scratch = %q, want the reloaded /transcode-new", scratch.Path)
	}
	if scratch.UsedGB < 400 {
		t.Fatalf("scratch used = %v GiB, want the new volume's near-full reading", scratch.UsedGB)
	}
}

// scratchDisk returns the disk carrying the scratch role, which is the one
// transcode admission reads.
func scratchDisk(t *testing.T, s *Sampler) DiskStats {
	t.Helper()
	for _, disk := range s.Snapshot().System.Disks {
		if disk.Role == ScratchDiskRole {
			return disk
		}
	}
	t.Fatalf("no disk carried the scratch role: %+v", s.Snapshot().System.Disks)
	return DiskStats{}
}

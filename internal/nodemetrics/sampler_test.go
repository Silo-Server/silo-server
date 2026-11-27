package nodemetrics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeClock advances only when a test says so, so every rate in this package is
// computed against an interval the test chose rather than against however long
// the test machine happened to take.
type fakeClock struct{ at time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time { return c.at }

func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// procTree builds a fake /proc under t.TempDir and returns its path.
type procTree struct {
	t    *testing.T
	root string
}

func newProcTree(t *testing.T) *procTree {
	t.Helper()
	return &procTree{t: t, root: t.TempDir()}
}

// write creates a file relative to the tree root, making parents as needed.
func (p *procTree) write(relPath, content string) {
	p.t.Helper()
	full := filepath.Join(p.root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		p.t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		p.t.Fatalf("write %s: %v", full, err)
	}
}

// writeFile writes body to dir/name and returns the full path.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

// writeIdleHost lays down an idle one-core /proc/stat, an idle loadavg, 1 MiB
// of memory and no network interfaces.
func (p *procTree) writeIdleHost() {
	p.t.Helper()
	p.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	p.write("loadavg", "0 0 0 0/0 0\n")
	p.write("meminfo", "MemTotal: 1024 kB\n")
	p.write("net/dev", "")
}

// newTestSampler wires a sampler onto a fake /proc with a fake clock and no
// real subprocess, statfs or hardware access.
func newTestSampler(t *testing.T, tree *procTree, clock *fakeClock, opts Options) *Sampler {
	t.Helper()
	if opts.Now == nil {
		opts.Now = clock.now
	}
	if opts.FFmpegChildren == nil {
		opts.FFmpegChildren = func() []int { return nil }
	}
	s := NewSampler(opts)
	s.goos = "linux"
	s.procDir = tree.root
	// Point hostProcDir at a path that does not exist by default so tests are
	// deterministic regardless of whether the machine running them happens to
	// have a real /host/proc; tests that exercise the lxcfs override set this
	// explicitly to a tree that does.
	s.hostProcDir = filepath.Join(tree.root, "no-such-host-proc")
	// Every cgroup source is cleared, not just the ones a given test sets: these
	// default to real host paths, and on Linux a test that forgot one reads the
	// machine it is running on instead of its fixture.
	s.cgroupUsagePaths = nil
	s.cgroupCPUPaths = nil
	s.cgroupCPUSetPaths = nil
	s.statfs = func(string) (fsStats, error) { return fsStats{}, os.ErrNotExist }
	s.runNVIDIASMI = func(context.Context) ([]byte, error) { return nil, os.ErrNotExist }
	return s
}

func TestCPUBusyPercentFromProcStatDeltas(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	// user nice system idle iowait irq softirq steal
	tree.write("stat", "cpu  100 0 100 800 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0\ncpu1 50 0 50 400 0 0 0 0\n")
	tree.write("loadavg", "3.20 1.10 0.90 2/512 9\n")
	tree.write("meminfo", "MemTotal: 1024 kB\nMemAvailable: 512 kB\n")
	tree.write("net/dev", "Inter-|\n face |\n")

	s := newTestSampler(t, tree, clock, Options{})
	s.sample(context.Background())
	if got := s.Snapshot().System.CPUPct; got != 0 {
		t.Fatalf("first sample CPUPct = %d, want 0 (no previous reading to diff against)", got)
	}

	// 400 jiffies of work out of 1000 elapsed.
	tree.write("stat", "cpu  300 0 300 1400 0 0 0 0\ncpu0 150 0 150 700 0 0 0 0\ncpu1 150 0 150 700 0 0 0 0\n")
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.CPUPct != 40 {
		t.Fatalf("CPUPct = %d, want 40", system.CPUPct)
	}
	if system.Cores != 2 {
		t.Fatalf("Cores = %d, want 2", system.Cores)
	}
	if system.Load1 != 3.20 {
		t.Fatalf("Load1 = %v, want 3.2", system.Load1)
	}
}

// When the cgroup usage file a pass reads from changes, the previous reading
// came from a different counter, so the two cannot be differenced. Here a
// cpuset pins the process to two of four cores; the leaf's usage file goes
// unreadable for a pass and the reading falls back to the root's, which
// carries the same two-core budget but counts far more CPU time.
func TestCgroupCPUStartsAFreshPairWhenTheUsageSourceChanges(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.writeIdleHost()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\ncpu0 0 0 0 0\ncpu1 0 0 0 0\ncpu2 0 0 0 0\ncpu3 0 0 0 0\n")
	tree.write("cgroup/cpuset.cpus.effective", "0-1\n")
	tree.write("cgroup/leaf/cpu.max", "max 100000\n")
	tree.write("cgroup/leaf/cpu.stat", "usage_usec 1000000\n")
	tree.write("cgroup/cpu.max", "max 100000\n")
	tree.write("cgroup/cpu.stat", "usage_usec 900000000\n")
	level := func(dir string) cgroupCPUPath {
		return cgroupCPUPath{
			usage:     filepath.Join(tree.root, dir, "cpu.stat"),
			usageKey:  cgroupCPUUsageKey,
			usageUnit: time.Microsecond,
			quota:     filepath.Join(tree.root, dir, "cpu.max"),
		}
	}

	s := newTestSampler(t, tree, clock, Options{})
	s.cgroupCPUPaths = []cgroupCPUPath{level("cgroup/leaf"), level("cgroup")}
	s.cgroupCPUSetPaths = []string{filepath.Join(tree.root, "cgroup/cpuset.cpus.effective")}
	s.sample(context.Background())

	if err := os.Remove(filepath.Join(tree.root, "cgroup/leaf/cpu.stat")); err != nil {
		t.Fatal(err)
	}
	clock.advance(5 * time.Second)
	s.sample(context.Background())
	snap := s.Snapshot()
	if snap.System.CPUPct != 0 || snap.Attribution.CPU.Available {
		t.Fatalf("CPUPct = %d (available %v) after the usage source changed, want 0 and unavailable", snap.System.CPUPct, snap.Attribution.CPU.Available)
	}

	// The root's own counter now has a baseline: 5s of CPU over 5s on a
	// two-core budget is half of it.
	tree.write("cgroup/cpu.stat", "usage_usec 905000000\n")
	clock.advance(5 * time.Second)
	s.sample(context.Background())
	snap = s.Snapshot()
	if snap.System.CPUPct != 50 || !snap.Attribution.CPU.Available {
		t.Fatalf("CPUPct = %d (available %v) on the second reading of one source, want 50 and available", snap.System.CPUPct, snap.Attribution.CPU.Available)
	}
}

// In a container /proc/meminfo describes the host. The cgroup limit is what the
// kernel will actually OOM-kill against, so it has to win.
func TestMemoryCorrectedByCgroupLimitAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		limitFile   string
		limitBody   string
		usageFile   string
		statFile    string
		statBody    string
		inactiveKey string
		wantTotalMB int64
		wantUsedMB  int64
		usageBody   string
	}{
		{
			name:        "cgroup v2",
			limitFile:   "memory.max",
			limitBody:   "2147483648\n", // 2 GiB
			usageFile:   "memory.current",
			usageBody:   "1073741824\n", // 1 GiB charged
			statFile:    "memory.stat",
			statBody:    "anon 536870912\ninactive_file 536870912\n",
			inactiveKey: "inactive_file",
			wantTotalMB: 2048,
			wantUsedMB:  512, // page cache does not count toward the working set
		},
		{
			name:        "cgroup v1",
			limitFile:   "memory.limit_in_bytes",
			limitBody:   "4294967296\n", // 4 GiB
			usageFile:   "memory.usage_in_bytes",
			usageBody:   "2147483648\n",
			statFile:    "memory.stat",
			statBody:    "total_inactive_file 1073741824\n",
			inactiveKey: cgroupInactiveFileKeyV1,
			wantTotalMB: 4096,
			wantUsedMB:  1024,
		},
		{
			name:        "no concrete limit falls back to the host",
			limitFile:   "memory.max",
			limitBody:   "max\n",
			usageFile:   "memory.current",
			usageBody:   "",
			statFile:    "memory.stat",
			statBody:    "",
			inactiveKey: "inactive_file",
			wantTotalMB: 8192,
			wantUsedMB:  2048,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := newProcTree(t)
			clock := newFakeClock()
			tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
			tree.write("loadavg", "0 0 0 0/0 0\n")
			tree.write("net/dev", "")
			tree.write("meminfo", "MemTotal:       8388608 kB\nMemAvailable:   6291456 kB\n")

			cgroupDir := t.TempDir()
			writeFile(t, cgroupDir, tc.limitFile, tc.limitBody)
			if tc.usageBody != "" {
				writeFile(t, cgroupDir, tc.usageFile, tc.usageBody)
				writeFile(t, cgroupDir, tc.statFile, tc.statBody)
			}

			s := newTestSampler(t, tree, clock, Options{})
			s.cgroupUsagePaths = []cgroupUsagePath{{
				limit:        filepath.Join(cgroupDir, tc.limitFile),
				usage:        filepath.Join(cgroupDir, tc.usageFile),
				stat:         filepath.Join(cgroupDir, tc.statFile),
				inactiveFile: tc.inactiveKey,
			}}
			s.sample(context.Background())

			system := s.Snapshot().System
			if system.MemTotalMB != tc.wantTotalMB {
				t.Fatalf("MemTotalMB = %d, want %d", system.MemTotalMB, tc.wantTotalMB)
			}
			if system.MemUsedMB != tc.wantUsedMB {
				t.Fatalf("MemUsedMB = %d, want %d", system.MemUsedMB, tc.wantUsedMB)
			}
		})
	}
}

// The exact JSON is a wire contract: node health responses, the admin resources
// endpoint, and the persisted last_stats column all carry this shape.
func TestSnapshotJSONShape(t *testing.T) {
	total := 71
	video, render := 63, 12
	vramUsed := int64(812)
	vramTotal := int64(8192)
	snapshot := Snapshot{
		Available: true,
		System: &SystemStats{
			CPUPct:     41,
			Load1:      3.2,
			Cores:      16,
			MemUsedMB:  9011,
			MemTotalMB: 32768,
			Disks: []DiskStats{
				{Path: "/transcode", UsedGB: 210, TotalGB: 500},
				{Path: "/media", UsedGB: 7100, TotalGB: 8000, Stale: true},
				{Path: "/gone", Unavailable: true},
			},
			NetRxBps: 1200000,
			NetTxBps: 98000000,
		},
		GPU: []GPUStats{{
			Device:        "/dev/dri/renderD128",
			Vendor:        "intel",
			Sessions:      2,
			VideoBusyPct:  &video,
			RenderBusyPct: &render,
			TotalBusyPct:  &total,
			VRAMUsedMB:    &vramUsed,
			VRAMTotalMB:   &vramTotal,
			Source:        SourceFdinfoNVIDIASMI,
		}},
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	system, ok := decoded["system"].(map[string]any)
	if !ok {
		t.Fatalf("system missing from %s", encoded)
	}
	for _, key := range []string{"cpu_pct", "load1", "cores", "mem_used_mb", "mem_total_mb", "disks", "net_rx_bps", "net_tx_bps"} {
		if _, ok := system[key]; !ok {
			t.Fatalf("system.%s missing from %s", key, encoded)
		}
	}
	disks := system["disks"].([]any)
	if _, ok := disks[0].(map[string]any)["stale"]; ok {
		t.Fatalf("stale emitted for a fresh disk: %s", encoded)
	}
	if stale, _ := disks[1].(map[string]any)["stale"].(bool); !stale {
		t.Fatalf("stale missing for a stale disk: %s", encoded)
	}
	if unavailable, _ := disks[2].(map[string]any)["unavailable"].(bool); !unavailable {
		t.Fatalf("unavailable missing: %s", encoded)
	}

	gpu := decoded["gpu"].([]any)[0].(map[string]any)
	for _, key := range []string{"device", "vendor", "sessions", "video_busy_pct", "render_busy_pct", "total_busy_pct", "vram_used_mb", "vram_total_mb", "source"} {
		if _, ok := gpu[key]; !ok {
			t.Fatalf("gpu.%s missing from %s", key, encoded)
		}
	}

	// Absent enrichment must be absent, not zero: an operator reading
	// total_busy_pct: 0 would conclude the GPU is idle when nothing measured it.
	bare, err := json.Marshal(Snapshot{Available: true, GPU: []GPUStats{{Device: "/dev/dri/renderD128", Source: SourceFdinfo}}})
	if err != nil {
		t.Fatal(err)
	}
	var bareDecoded map[string]any
	if err := json.Unmarshal(bare, &bareDecoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := bareDecoded["system"]; ok {
		t.Fatalf("system emitted when absent: %s", bare)
	}
	bareGPU := bareDecoded["gpu"].([]any)[0].(map[string]any)
	for _, key := range []string{"video_busy_pct", "render_busy_pct", "total_busy_pct", "vram_used_mb", "vram_total_mb", "vendor"} {
		if _, ok := bareGPU[key]; ok {
			t.Fatalf("gpu.%s emitted when absent: %s", key, bare)
		}
	}
}

// On an LXC host running Docker nested inside it, this process's own /proc
// sees the raw kernel — the bare-metal core count, memory, and load — while
// its own cgroup is unlimited, because the LXC's cap lives on an ancestor
// cgroup outside its namespace. lxcfs virtualizes /proc/stat, /proc/loadavg,
// and /proc/meminfo to the LXC's own limits, so when those are bind-mounted
// in at hostProcDir, they must win over the container's raw /proc for exactly
// those three files.
func TestHostProcOverridesLxcfsScopedFiles(t *testing.T) {
	tree := newProcTree(t)
	hostTree := newProcTree(t)
	clock := newFakeClock()

	// The nested container's own /proc: a busy 8-core bare-metal host, 64 GiB
	// of memory, and a load average that belongs to every other tenant on the
	// box too.
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n"+
		"cpu0 0 0 0 0 0 0 0 0\ncpu1 0 0 0 0 0 0 0 0\ncpu2 0 0 0 0 0 0 0 0\ncpu3 0 0 0 0 0 0 0 0\n"+
		"cpu4 0 0 0 0 0 0 0 0\ncpu5 0 0 0 0 0 0 0 0\ncpu6 0 0 0 0 0 0 0 0\ncpu7 0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "40.00 38.00 35.00 12/900 5555\n")
	tree.write("meminfo", "MemTotal: 67108864 kB\nMemAvailable: 33554432 kB\n")
	tree.write("net/dev", "Inter-|\n face |\n")

	// lxcfs's view, bind-mounted at hostProcDir: the LXC is capped at 2 cores
	// and 2 GiB, and its own load average.
	hostTree.write("stat", "cpu  100 0 100 800 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0\ncpu1 50 0 50 400 0 0 0 0\n")
	hostTree.write("loadavg", "1.50 1.20 0.80 1/64 42\n")
	hostTree.write("meminfo", "MemTotal: 2097152 kB\nMemAvailable: 1048576 kB\n")

	s := newTestSampler(t, tree, clock, Options{})
	s.hostProcDir = hostTree.root
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.Cores != 2 {
		t.Fatalf("Cores = %d, want the lxcfs-scoped 2, not the host's 8", system.Cores)
	}
	if system.Load1 != 1.50 {
		t.Fatalf("Load1 = %v, want the lxcfs-scoped 1.50, not the raw host's 40.00", system.Load1)
	}
	if system.MemTotalMB != 2048 {
		t.Fatalf("MemTotalMB = %d, want the lxcfs-scoped 2048, not the raw host's 65536", system.MemTotalMB)
	}
	if system.MemUsedMB != 1024 {
		t.Fatalf("MemUsedMB = %d, want 1024 (2048 total - 1024 available)", system.MemUsedMB)
	}

	// A second sample drives the CPU busy-percent delta off the lxcfs stat
	// file too: 400 more busy jiffies out of 1000 more total.
	hostTree.write("stat", "cpu  300 0 300 1400 0 0 0 0\ncpu0 150 0 150 700 0 0 0 0\ncpu1 150 0 150 700 0 0 0 0\n")
	tree.write("stat", "cpu  99999 0 0 1 0 0 0 0\n") // the raw host is pegged; must not be read
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system = s.Snapshot().System
	if system.CPUPct != 40 {
		t.Fatalf("CPUPct = %d, want 40 from the lxcfs stat deltas, not the raw host's near-100", system.CPUPct)
	}
	if system.Cores != 2 {
		t.Fatalf("Cores = %d, want 2 on the second sample too", system.Cores)
	}
}

// A container with no memory limit still publishes a readable memory.current.
// Taking it unconditionally paired this process's working set with the host's
// RAM — "1 GiB of 64 GiB" on a machine that is nearly out of memory, because
// the two numbers come from different domains.
func TestMemoryStatsDoesNotMixCgroupUsageWithHostTotal(t *testing.T) {
	tree := newProcTree(t)
	tree.write("meminfo", "MemTotal: 65536 kB\nMemAvailable: 1024 kB\n")
	s := newTestSampler(t, tree, newFakeClock(), Options{})

	// No limit file resolves, which is what an unconstrained container looks
	// like, but the usage file is readable.
	usage := filepath.Join(t.TempDir(), "memory.current")
	if err := os.WriteFile(usage, []byte("1048576\n"), 0o600); err != nil {
		t.Fatalf("write cgroup usage: %v", err)
	}
	s.cgroupUsagePaths = []cgroupUsagePath{{limit: filepath.Join(t.TempDir(), "absent"), usage: usage}}

	used, total := s.memoryStats()
	if total != 65536*1024 {
		t.Fatalf("total = %d, want the host's MemTotal", total)
	}
	// MemTotal - MemAvailable, from the same file as total.
	if want := int64(64512 * 1024); used != want {
		t.Fatalf("used = %d, want the host's used figure %d rather than the cgroup working set", used, want)
	}
}

// A leaf cgroup that imposes no memory limit does not mean the process has
// none: a systemd unit inside a slice with MemoryMax=, or a container under a
// limited pod cgroup, is capped by an ancestor while its own memory.max reads
// "max". Taking the first readable limit would report the host's RAM to a
// process the kernel will OOM-kill at 2 GiB.
func TestMemoryLimitTakesTheTightestCgroupInForce(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 67108864 kB\nMemAvailable: 33554432 kB\n")
	tree.write("net/dev", "")

	dir := t.TempDir()
	write := func(name, body string) string { return writeFile(t, dir, name, body) }
	// Leaf first, as the resolved list is ordered. The leaf allows 8 GiB, but the
	// slice containing it allows only 2 — so the first readable limit is not the
	// one the kernel kills against, which is what makes this more than an
	// ordering detail.
	leaf := write("leaf.max", "8589934592\n")
	slice := write("slice.max", "2147483648\n")
	root := write("root.max", "68719476736\n")

	// The slice's usage counts every service under it, not just this one: that
	// is the population its limit actually bounds.
	leafUsage := write("leaf.current", "1073741824\n")
	sliceUsage := write("slice.current", "1610612736\n")
	rootUsage := write("root.current", "3221225472\n")
	stat := write("memory.stat", "inactive_file 0\n")

	s := newTestSampler(t, tree, clock, Options{})
	s.cgroupUsagePaths = []cgroupUsagePath{
		{limit: leaf, usage: leafUsage, stat: stat, inactiveFile: cgroupInactiveFileKeyV2},
		{limit: slice, usage: sliceUsage, stat: stat, inactiveFile: cgroupInactiveFileKeyV2},
		{limit: root, usage: rootUsage, stat: stat, inactiveFile: cgroupInactiveFileKeyV2},
	}
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.MemTotalMB != 2048 {
		t.Fatalf("MemTotalMB = %d, want the 2048 the slice allows, not the leaf's looser 8192", system.MemTotalMB)
	}
	// Used comes from the *same* cgroup the limit did. Pairing the slice's
	// 2 GiB capacity with this service's own 1 GiB would show half the volume
	// free while the sibling services under that slice have filled it.
	if system.MemUsedMB != 1536 {
		t.Fatalf("MemUsedMB = %d, want the 1536 charged to the slice whose limit binds", system.MemUsedMB)
	}

	// The other shape of the same bug: a leaf that imposes nothing at all while
	// an ancestor does.
	unlimited := write("unlimited.max", "max\n")
	s = newTestSampler(t, tree, clock, Options{})
	s.cgroupUsagePaths = []cgroupUsagePath{
		{limit: unlimited, usage: leafUsage, stat: stat, inactiveFile: cgroupInactiveFileKeyV2},
		{limit: slice, usage: sliceUsage, stat: stat, inactiveFile: cgroupInactiveFileKeyV2},
		{limit: root, usage: rootUsage, stat: stat, inactiveFile: cgroupInactiveFileKeyV2},
	}
	s.sample(context.Background())
	if got := s.Snapshot().System.MemTotalMB; got != 2048 {
		t.Fatalf("MemTotalMB = %d with an unlimited leaf, want the slice's 2048", got)
	}
}

// Two cgroups publishing the same memory limit are not equivalent: the
// ancestor's is shared with siblings that can fill it, so it is the one whose
// usage says how much is left. Reading the leaf instead shows headroom right up
// until the parent OOMs — the mirror of the CPU quota tie-break.
func TestMemoryLimitPrefersTheOuterCgroupOnATie(t *testing.T) {
	tree := newProcTree(t)
	tree.write("meminfo", "MemTotal: 67108864 kB\nMemAvailable: 33554432 kB\n")
	s := newTestSampler(t, tree, newFakeClock(), Options{})

	dir := t.TempDir()
	write := func(name, body string) string { return writeFile(t, dir, name, body) }
	const twoGiB = "2147483648\n"
	stat := write("memory.stat", "inactive_file 0\n")
	s.cgroupUsagePaths = []cgroupUsagePath{
		{
			limit: write("leaf.max", twoGiB), usage: write("leaf.current", "536870912\n"),
			stat: stat, inactiveFile: cgroupInactiveFileKeyV2,
		},
		{
			limit: write("slice.max", twoGiB), usage: write("slice.current", "1879048192\n"),
			stat: stat, inactiveFile: cgroupInactiveFileKeyV2,
		},
	}

	used, total := s.memoryStats()
	if total != 2147483648 {
		t.Fatalf("total = %d, want the 2 GiB both levels publish", total)
	}
	if want := int64(1879048192); used != want {
		t.Fatalf("used = %d, want the %d charged to the shared parent, not this service's own", used, want)
	}
}

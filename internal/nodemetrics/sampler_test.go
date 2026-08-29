package nodemetrics

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// A counter that went backwards means the two readings do not describe one
// continuous run. Reporting the difference would produce a nonsense percentage.
func TestCPUBusyPercentClampsCounterReset(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  1000 0 1000 8000 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")

	s := newTestSampler(t, tree, clock, Options{})
	s.sample(context.Background())

	tree.write("stat", "cpu  10 0 10 80 0 0 0 0\n")
	clock.advance(5 * time.Second)
	s.sample(context.Background())
	if got := s.Snapshot().System.CPUPct; got != 0 {
		t.Fatalf("CPUPct after counter reset = %d, want 0", got)
	}

	// The reset reading becomes the new baseline, so the next interval is
	// measured normally rather than against the pre-reset counter.
	tree.write("stat", "cpu  60 0 60 180 0 0 0 0\n")
	clock.advance(5 * time.Second)
	s.sample(context.Background())
	if got := s.Snapshot().System.CPUPct; got != 50 {
		t.Fatalf("CPUPct after re-baselining = %d, want 50", got)
	}
}

func TestNetworkThroughputExcludesLoopbackAndClampsResets(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	netDev := func(loRx, eth0Rx, eth0Tx int) string {
		return "Inter-|   Receive                    |  Transmit\n" +
			" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
			"    lo: " + itoa(loRx) + " 1 0 0 0 0 0 0 " + itoa(loRx) + " 1 0 0 0 0 0 0\n" +
			"  eth0: " + itoa(eth0Rx) + " 1 0 0 0 0 0 0 " + itoa(eth0Tx) + " 1 0 0 0 0 0 0\n"
	}
	tree.write("net/dev", netDev(1_000_000, 1000, 2000))

	s := newTestSampler(t, tree, clock, Options{})
	s.sample(context.Background())

	// eth0 gains 5000 rx and 10000 tx bytes over 5s; loopback gains a lot and
	// must not appear.
	tree.write("net/dev", netDev(99_000_000, 6000, 12000))
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.NetRxBps != 8000 { // 5000 bytes / 5s * 8
		t.Fatalf("NetRxBps = %d, want 8000", system.NetRxBps)
	}
	if system.NetTxBps != 16000 { // 10000 bytes / 5s * 8
		t.Fatalf("NetTxBps = %d, want 16000", system.NetTxBps)
	}

	// An interface restarting resets the aggregate; that must read as zero, not
	// as a negative or a wrapped spike.
	tree.write("net/dev", netDev(99_000_000, 10, 20))
	clock.advance(5 * time.Second)
	s.sample(context.Background())
	system = s.Snapshot().System
	if system.NetRxBps != 0 || system.NetTxBps != 0 {
		t.Fatalf("throughput after counter reset = %d/%d, want 0/0", system.NetRxBps, system.NetTxBps)
	}
}

// An interface entering the namespace mid-run — a veth or tunnel moved into a
// running container — arrives carrying its lifetime totals. With an aggregate
// baseline those totals would read as one interval's traffic; per-interface
// pairing gives the newcomer this reading as its baseline and counts it from
// the next interval, while an interface leaving takes only its own baseline
// with it instead of masking the survivors behind a shrunken aggregate.
func TestNetworkThroughputSurvivesInterfaceChurn(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	header := "Inter-|   Receive                    |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"
	iface := func(name string, rx, tx int) string {
		return "  " + name + ": " + itoa(rx) + " 1 0 0 0 0 0 0 " + itoa(tx) + " 1 0 0 0 0 0 0\n"
	}

	tree.write("net/dev", header+iface("eth0", 1000, 2000))
	s := newTestSampler(t, tree, clock, Options{})
	s.sample(context.Background())

	// tailscale0 appears between readings carrying 40 GB of lifetime history;
	// eth0 moves by its usual 5000/10000. Only eth0's delta may appear.
	tree.write("net/dev", header+iface("eth0", 6000, 12000)+iface("tailscale0", 40_000_000_000, 40_000_000_000))
	clock.advance(5 * time.Second)
	s.sample(context.Background())
	system := s.Snapshot().System
	if system.NetRxBps != 8000 || system.NetTxBps != 16000 {
		t.Fatalf("throughput with a newborn interface = %d/%d, want 8000/16000", system.NetRxBps, system.NetTxBps)
	}

	// From its second reading the newcomer has a baseline and counts.
	tree.write("net/dev", header+iface("eth0", 11000, 22000)+iface("tailscale0", 40_000_005_000, 40_000_010_000))
	clock.advance(5 * time.Second)
	s.sample(context.Background())
	system = s.Snapshot().System
	if system.NetRxBps != 16000 || system.NetTxBps != 32000 {
		t.Fatalf("throughput once the newcomer has a baseline = %d/%d, want 16000/32000", system.NetRxBps, system.NetTxBps)
	}

	// The newcomer vanishes again. Its departure must not hide eth0's real
	// traffic, even though the aggregate across interfaces plummeted.
	tree.write("net/dev", header+iface("eth0", 16000, 32000))
	clock.advance(5 * time.Second)
	s.sample(context.Background())
	system = s.Snapshot().System
	if system.NetRxBps != 8000 || system.NetTxBps != 16000 {
		t.Fatalf("throughput after an interface left = %d/%d, want 8000/16000", system.NetRxBps, system.NetTxBps)
	}
}

func TestMemoryFromMeminfo(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("net/dev", "")
	// 8 GiB total, 6 GiB available.
	tree.write("meminfo", "MemTotal:       8388608 kB\nMemFree:         512000 kB\nMemAvailable:   6291456 kB\n")

	s := newTestSampler(t, tree, clock, Options{})
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.MemTotalMB != 8192 {
		t.Fatalf("MemTotalMB = %d, want 8192", system.MemTotalMB)
	}
	if system.MemUsedMB != 2048 {
		t.Fatalf("MemUsedMB = %d, want 2048", system.MemUsedMB)
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
			if err := os.WriteFile(filepath.Join(cgroupDir, tc.limitFile), []byte(tc.limitBody), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.usageBody != "" {
				if err := os.WriteFile(filepath.Join(cgroupDir, tc.usageFile), []byte(tc.usageBody), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cgroupDir, tc.statFile), []byte(tc.statBody), 0o644); err != nil {
					t.Fatal(err)
				}
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

// In a container /proc/stat describes the host, exactly as /proc/meminfo does.
// A node capped at two cores of a mostly idle 8-core host and pinned at its
// quota is the state worth alerting on, and reading the host would report it as
// nearly idle.
func TestCPUCorrectedByCgroupQuotaAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		paths      cgroupCPUPath
		quotaFile  string
		quotaBody  string
		usageFile  string
		usageFirst string
		usageLater string
		periodFile string
		periodBody string
	}{
		{
			name:       "cgroup v2",
			paths:      cgroupCPUPath{usageKey: cgroupCPUUsageKey, usageUnit: time.Microsecond},
			usageFile:  "cpu.stat",
			usageFirst: "usage_usec 1000000\nuser_usec 500000\n",
			usageLater: "usage_usec 11000000\nuser_usec 5000000\n", // +10s of CPU
			quotaFile:  "cpu.max",
			quotaBody:  "200000 100000\n", // 2 cores
		},
		{
			name:       "cgroup v1",
			paths:      cgroupCPUPath{usageUnit: time.Nanosecond},
			usageFile:  "cpuacct.usage",
			usageFirst: "1000000000\n",
			usageLater: "11000000000\n", // +10s of CPU
			quotaFile:  "cpu.cfs_quota_us",
			quotaBody:  "200000\n",
			periodFile: "cpu.cfs_period_us",
			periodBody: "100000\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := newProcTree(t)
			clock := newFakeClock()
			// The host has 8 cores and is barely busy; the container is not.
			hostStat := func(busy, idle int) string {
				line := "cpu  " + itoa(busy) + " 0 0 " + itoa(idle) + " 0 0 0 0\n"
				for i := range 8 {
					line += "cpu" + itoa(i) + " 0 0 0 0 0 0 0 0\n"
				}
				return line
			}
			tree.write("stat", hostStat(100, 9900))
			tree.write("loadavg", "0 0 0 0/0 0\n")
			tree.write("meminfo", "MemTotal: 1024 kB\n")
			tree.write("net/dev", "")

			cgroupDir := t.TempDir()
			writeCgroup := func(name, body string) {
				if err := os.WriteFile(filepath.Join(cgroupDir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			writeCgroup(tc.usageFile, tc.usageFirst)
			writeCgroup(tc.quotaFile, tc.quotaBody)
			paths := tc.paths
			paths.usage = filepath.Join(cgroupDir, tc.usageFile)
			paths.quota = filepath.Join(cgroupDir, tc.quotaFile)
			if tc.periodFile != "" {
				writeCgroup(tc.periodFile, tc.periodBody)
				paths.period = filepath.Join(cgroupDir, tc.periodFile)
			}

			s := newTestSampler(t, tree, clock, Options{})
			s.cgroupCPUPaths = []cgroupCPUPath{paths}
			s.sample(context.Background())
			if got := s.Snapshot().System.CPUPct; got != 0 {
				t.Fatalf("first sample CPUPct = %d, want 0 (nothing to diff against)", got)
			}

			// 10 seconds of CPU over 5 seconds of wall time on a 2-core quota:
			// the container is pegged, while the host is 1% busy.
			writeCgroup(tc.usageFile, tc.usageLater)
			tree.write("stat", hostStat(200, 19800))
			clock.advance(5 * time.Second)
			s.sample(context.Background())

			system := s.Snapshot().System
			if system.CPUPct != 100 {
				t.Fatalf("CPUPct = %d, want 100 (the container's own usage against its own quota)", system.CPUPct)
			}
			if system.Cores != 2 {
				t.Fatalf("Cores = %d, want the 2 cores the cgroup allows, not the host's 8", system.Cores)
			}
		})
	}
}

// An unconstrained cgroup measures this process alone, which is not what a node
// competes for: uncapped, the CPU a neighbor burns is CPU this node cannot have.
// So with no quota the host's own busyness stands, even though the cgroup could
// be read — here Silo uses a quarter of the machine while the machine is at
// three quarters.
func TestCPUWithoutCgroupQuotaReportsTheHost(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  100 0 0 900 0 0 0 0\ncpu0 0 0 0 0 0 0 0 0\ncpu1 0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")

	cgroupDir := t.TempDir()
	usage := filepath.Join(cgroupDir, "cpu.stat")
	if err := os.WriteFile(usage, []byte("usage_usec 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cgroupDir, "cpu.max"), []byte("max 100000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestSampler(t, tree, clock, Options{})
	s.cgroupCPUPaths = []cgroupCPUPath{{
		usage:     usage,
		usageKey:  cgroupCPUUsageKey,
		usageUnit: time.Microsecond,
		quota:     filepath.Join(cgroupDir, "cpu.max"),
	}}
	s.sample(context.Background())

	// 5 seconds of CPU over 5 seconds of wall time across 2 cores: half of what
	// this process could use, and a quarter of the two-core machine.
	if err := os.WriteFile(usage, []byte("usage_usec 5000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The host meanwhile spent 750 of the interval's 1000 ticks busy.
	tree.write("stat", "cpu  850 0 0 1150 0 0 0 0\ncpu0 0 0 0 0 0 0 0 0\ncpu1 0 0 0 0 0 0 0 0\n")
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.CPUPct != 75 {
		t.Fatalf("CPUPct = %d, want the host's 75 rather than this process's 25", system.CPUPct)
	}
	if system.Cores != 2 {
		t.Fatalf("Cores = %d, want the host's 2 with no quota set", system.Cores)
	}
}

// A host that cannot be sampled must publish an explicitly unavailable
// snapshot, so a reader can distinguish it from a host that is simply idle.
func TestNonLinuxHostReportsUnavailable(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	s := newTestSampler(t, tree, clock, Options{})
	s.goos = "darwin"
	s.sample(context.Background())

	snapshot := s.Snapshot()
	if snapshot.Available {
		t.Fatal("Available = true on a non-Linux host")
	}
	if snapshot.System != nil || snapshot.GPU != nil {
		t.Fatalf("snapshot carries data on a non-Linux host: %+v", snapshot)
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

// Without a bind-mounted lxcfs view — plain Docker, bare metal, or an LXC
// deployment that has not mounted /host/proc — every reading must fall back
// to the container's own /proc exactly as before this feature existed.
func TestHostProcAbsentFallsBackToProcDir(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  100 0 100 800 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0\ncpu1 50 0 50 400 0 0 0 0\n")
	tree.write("loadavg", "3.20 1.10 0.90 2/512 9\n")
	tree.write("meminfo", "MemTotal: 8388608 kB\nMemAvailable: 6291456 kB\n")
	tree.write("net/dev", "Inter-|\n face |\n")

	// newTestSampler already points hostProcDir at a nonexistent path; this
	// test just makes that fallback explicit and asserts on it.
	s := newTestSampler(t, tree, clock, Options{})
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.Cores != 2 {
		t.Fatalf("Cores = %d, want 2 from procDir", system.Cores)
	}
	if system.Load1 != 3.20 {
		t.Fatalf("Load1 = %v, want 3.2 from procDir", system.Load1)
	}
	if system.MemTotalMB != 8192 {
		t.Fatalf("MemTotalMB = %d, want 8192 from procDir", system.MemTotalMB)
	}
	if system.MemUsedMB != 2048 {
		t.Fatalf("MemUsedMB = %d, want 2048 from procDir", system.MemUsedMB)
	}
}

// Only stat, loadavg, and meminfo are host-proc aware. net/dev is per-netns
// and must stay on the container's own /proc even when hostProcDir has its
// own net/dev sitting right next to the other three files — otherwise a
// nested node would report the LXC host's aggregate network traffic instead
// of its own.
func TestHostProcDoesNotAffectNetDev(t *testing.T) {
	tree := newProcTree(t)
	hostTree := newProcTree(t)
	clock := newFakeClock()

	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	netDev := func(rx, tx int) string {
		return "Inter-|   Receive                    |  Transmit\n" +
			" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
			"  eth0: " + itoa(rx) + " 1 0 0 0 0 0 0 " + itoa(tx) + " 1 0 0 0 0 0 0\n"
	}
	tree.write("net/dev", netDev(1000, 2000))

	// hostTree has its own net/dev with very different counters. If it were
	// ever consulted, the throughput computed below would not match.
	hostTree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	hostTree.write("loadavg", "0 0 0 0/0 0\n")
	hostTree.write("meminfo", "MemTotal: 1024 kB\n")
	hostTree.write("net/dev", netDev(9_000_000, 9_000_000))

	s := newTestSampler(t, tree, clock, Options{})
	s.hostProcDir = hostTree.root
	s.sample(context.Background())

	tree.write("net/dev", netDev(6000, 12000))
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.NetRxBps != 8000 { // 5000 bytes / 5s * 8, from tree's net/dev
		t.Fatalf("NetRxBps = %d, want 8000 from procDir's net/dev, not hostProcDir's", system.NetRxBps)
	}
	if system.NetTxBps != 16000 { // 10000 bytes / 5s * 8
		t.Fatalf("NetTxBps = %d, want 16000 from procDir's net/dev, not hostProcDir's", system.NetTxBps)
	}
}

// itoa avoids importing strconv into every fixture builder above.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
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

// With a concrete limit both numbers come from the cgroup, which is the whole
// point of the correction.
func TestMemoryStatsUsesCgroupUsageBesideACgroupLimit(t *testing.T) {
	tree := newProcTree(t)
	tree.write("meminfo", "MemTotal: 65536 kB\nMemAvailable: 1024 kB\n")
	s := newTestSampler(t, tree, newFakeClock(), Options{})

	dir := t.TempDir()
	limit := filepath.Join(dir, "memory.max")
	if err := os.WriteFile(limit, []byte("8388608\n"), 0o600); err != nil {
		t.Fatalf("write cgroup limit: %v", err)
	}
	usage := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(usage, []byte("1048576\n"), 0o600); err != nil {
		t.Fatalf("write cgroup usage: %v", err)
	}
	s.cgroupUsagePaths = []cgroupUsagePath{{limit: limit, usage: usage}}

	used, total := s.memoryStats()
	if total != 8388608 {
		t.Fatalf("total = %d, want the cgroup limit", total)
	}
	if used != 1048576 {
		t.Fatalf("used = %d, want the cgroup working set", used)
	}
}

// NVENC workloads are keyed by CUDA index or GPU uuid, and only the nvidia-smi
// step supplies those aliases. With that step failed — a timeout, a missing
// toolkit, a tripped breaker — an NVENC node reported zero sessions while it
// transcoded, and one exposing no readable render node vanished from the sample
// altogether. Session accounting comes from the playback allocator, not a
// driver, so it is true regardless.
func TestSampleGPUReportsSessionsWhenNVIDIAEnrichmentFails(t *testing.T) {
	tree := newProcTree(t)
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	s := newTestSampler(t, tree, newFakeClock(), Options{})
	s.runNVIDIASMI = func(context.Context) ([]byte, error) {
		return nil, errors.New("nvidia-smi not installed")
	}
	s.identities = func() []DeviceIdentity { return nil }
	s.sessions = func() map[string]int { return map[string]int{"cuda:0": 2} }

	gpus := s.sampleGPU(context.Background(), s.now())

	if len(gpus) != 1 {
		t.Fatalf("gpu sample = %+v, want the known workload's device reported", gpus)
	}
	if gpus[0].Device != "cuda:0" {
		t.Fatalf("device = %q, want the name the workload is counted under", gpus[0].Device)
	}
	if gpus[0].Sessions != 2 {
		t.Fatalf("sessions = %d, want the allocator's count", gpus[0].Sessions)
	}
	if gpus[0].Source != SourceUnavailable {
		t.Fatalf("source = %q, want it reported as unmeasured", gpus[0].Source)
	}
}

// A workload whose device an enrichment source did name is not duplicated: the
// alias set already covers it.
func TestSampleGPUDoesNotDuplicateAnAlreadyNamedDevice(t *testing.T) {
	tree := newProcTree(t)
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	s := newTestSampler(t, tree, newFakeClock(), Options{})
	s.runNVIDIASMI = func(context.Context) ([]byte, error) {
		return []byte("0, GPU-x, 00000000:03:00.0, 71, 63, 12, 812, 8192\n"), nil
	}
	s.identities = func() []DeviceIdentity { return nil }
	s.sessions = func() map[string]int { return map[string]int{"cuda:0": 2} }

	gpus := s.sampleGPU(context.Background(), s.now())

	if len(gpus) != 1 {
		t.Fatalf("gpu sample = %+v, want one entry for the enriched device", gpus)
	}
	if gpus[0].Sessions != 2 {
		t.Fatalf("sessions = %d, want the workload joined onto the enriched entry", gpus[0].Sessions)
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
	write := func(name, body string) string {
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return full
	}
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

// A cpuset is the other way a deployment caps CPU, and it leaves cpu.max saying
// "max". A process pinned to two CPUs on a sixty-four core host would otherwise
// divide its own busy time by sixty-four and report three percent while it is
// saturated.
func TestCPUCorrectedByCpusetWithoutAQuota(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	// An eight-core host, so the two allowed CPUs are visibly a subset.
	hostStat := func(busy, idle int) string {
		line := "cpu  " + itoa(busy) + " 0 0 " + itoa(idle) + " 0 0 0 0\n"
		for i := range 8 {
			line += "cpu" + itoa(i) + " 0 0 0 0 0 0 0 0\n"
		}
		return line
	}
	tree.write("stat", hostStat(100, 9900))

	dir := t.TempDir()
	cpuset := filepath.Join(dir, "cpuset.cpus.effective")
	if err := os.WriteFile(cpuset, []byte("2-3\n"), 0o644); err != nil {
		t.Fatalf("write cpuset: %v", err)
	}
	usage := filepath.Join(dir, "cpu.stat")
	writeUsage := func(micros int) {
		if err := os.WriteFile(usage, []byte("usage_usec "+itoa(micros)+"\n"), 0o644); err != nil {
			t.Fatalf("write cgroup usage: %v", err)
		}
	}
	writeUsage(0)
	quota := filepath.Join(dir, "cpu.max")
	if err := os.WriteFile(quota, []byte("max 100000\n"), 0o644); err != nil {
		t.Fatalf("write cgroup quota: %v", err)
	}

	s := newTestSampler(t, tree, clock, Options{})
	s.cgroupCPUPaths = []cgroupCPUPath{{
		usage: usage, usageKey: cgroupCPUUsageKey, usageUnit: time.Microsecond, quota: quota,
	}}
	s.cgroupCPUSetPaths = []string{cpuset}
	s.sample(context.Background())

	// 10 seconds of CPU over 5 seconds of wall time on two allowed CPUs: pegged,
	// while the host is 1% busy.
	writeUsage(10_000_000)
	tree.write("stat", hostStat(200, 19800))
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.CPUPct != 100 {
		t.Fatalf("CPUPct = %d, want 100 — the process is saturating every CPU it may use", system.CPUPct)
	}
	if system.Cores != 2 {
		t.Fatalf("Cores = %d, want the 2 CPUs the cpuset allows, not the host's 8", system.Cores)
	}
}

func TestCountCPUSetEntries(t *testing.T) {
	for _, tc := range []struct {
		list string
		want int
	}{
		{"0-3", 4},
		{"0-3,8,12-13", 7},
		{"5", 1},
		{" 0-1 , 4 \n", 3},
		{"", 0},
		{"garbage", 0},
		// A reversed or negative range describes nothing; counting it would
		// invent a budget out of a malformed file.
		{"5-2", 0},
		{"-1", 0},
	} {
		t.Run(tc.list, func(t *testing.T) {
			if got := countCPUSetEntries(tc.list); got != tc.want {
				t.Fatalf("countCPUSetEntries(%q) = %d, want %d", tc.list, got, tc.want)
			}
		})
	}
}

// Every cgroup source defaults to a real host path, so a fixture that forgets
// one silently measures the machine the tests run on. That is invisible on a
// developer's macOS laptop and decides the result on a Linux CI runner — which
// is exactly how TestCPUWithoutCgroupQuotaUsesHostCores started failing only in
// CI. This asserts the constructor leaves nothing pointing at the system.
func TestNewTestSamplerReadsNoRealCgroupPaths(t *testing.T) {
	s := newTestSampler(t, newProcTree(t), newFakeClock(), Options{})

	var configured []string
	for _, level := range s.cgroupUsagePaths {
		configured = append(configured, level.limit, level.usage, level.stat)
	}
	for _, level := range s.cgroupCPUPaths {
		configured = append(configured, level.usage, level.quota, level.period)
	}
	configured = append(configured, s.cgroupCPUSetPaths...)
	configured = append(configured, s.procDir, s.hostProcDir)

	for _, path := range configured {
		if path == "" {
			continue
		}
		if strings.HasPrefix(path, cgroupMountRoot) || path == "/proc" {
			t.Fatalf("test sampler reads the real host path %q", path)
		}
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
	write := func(name, body string) string {
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return full
	}
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

// A cgroup limit that merely equals the host's memory is no limit at all, and
// must not start reading cgroup usage just because the tie-break loosened.
func TestMemoryLimitEqualToHostRAMIsNotALimit(t *testing.T) {
	tree := newProcTree(t)
	tree.write("meminfo", "MemTotal: 65536 kB\nMemAvailable: 1024 kB\n")
	s := newTestSampler(t, tree, newFakeClock(), Options{})

	dir := t.TempDir()
	write := func(name, body string) string {
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return full
	}
	s.cgroupUsagePaths = []cgroupUsagePath{{
		limit: write("memory.max", "67108864\n"), // exactly MemTotal
		usage: write("memory.current", "1048576\n"),
		stat:  write("memory.stat", "inactive_file 0\n"), inactiveFile: cgroupInactiveFileKeyV2,
	}}

	used, total := s.memoryStats()
	if total != 65536*1024 {
		t.Fatalf("total = %d, want the host's MemTotal", total)
	}
	if want := int64(64512 * 1024); used != want {
		t.Fatalf("used = %d, want the host's used figure %d rather than the cgroup working set", used, want)
	}
}

// A quota the machine cannot supply is not a limit — a 128-core quota on a
// 64-core host cannot be spent — so it must not move the measurement into the
// cgroup either. It is the same rule a cpuset spanning the host gets: an idle
// Silo beside a saturated neighbor would otherwise report its own few percent
// as the machine's load, from a quota that restricts it in no way at all.
func TestCPUQuotaAboveHostCapacityReportsTheHost(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	// A four-core host.
	hostStat := func(busy, idle int) string {
		line := "cpu  " + itoa(busy) + " 0 0 " + itoa(idle) + " 0 0 0 0\n"
		for i := range 4 {
			line += "cpu" + itoa(i) + " 0 0 0 0 0 0 0 0\n"
		}
		return line
	}
	tree.write("stat", hostStat(100, 900))

	dir := t.TempDir()
	usage := filepath.Join(dir, "cpu.stat")
	writeUsage := func(micros int) {
		if err := os.WriteFile(usage, []byte("usage_usec "+itoa(micros)+"\n"), 0o644); err != nil {
			t.Fatalf("write cgroup usage: %v", err)
		}
	}
	writeUsage(0)
	quota := filepath.Join(dir, "cpu.max")
	// Eight cores of quota on a four-core host.
	if err := os.WriteFile(quota, []byte("800000 100000\n"), 0o644); err != nil {
		t.Fatalf("write cgroup quota: %v", err)
	}

	s := newTestSampler(t, tree, clock, Options{})
	s.cgroupCPUPaths = []cgroupCPUPath{{
		usage: usage, usageKey: cgroupCPUUsageKey, usageUnit: time.Microsecond, quota: quota,
	}}
	s.sample(context.Background())

	// Silo spends 5 of the interval's 20 core-seconds — a quarter of the host —
	// while the host itself is three quarters busy with someone else's work.
	writeUsage(5_000_000)
	tree.write("stat", hostStat(850, 1150))
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.Cores != 4 {
		t.Fatalf("Cores = %d, want the host's 4 — an 8-core quota is not a limit here", system.Cores)
	}
	if system.CPUPct != 75 {
		t.Fatalf("CPUPct = %d, want the host's 75 rather than this process's 25", system.CPUPct)
	}
}

// Every unconstrained container and systemd service publishes an effective
// cpuset holding every online CPU, because it inherits the root's. Reading that
// as a two-CPU-style restriction would move the CPU reading to this cgroup's own
// usage on a host where nothing caps it, and Silo idling beside a saturated
// neighbor would report Silo's few percent as the machine's load.
func TestCPUIgnoresACpusetSpanningTheWholeHost(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	hostStat := func(busy, idle int) string {
		line := "cpu  " + itoa(busy) + " 0 0 " + itoa(idle) + " 0 0 0 0\n"
		for i := range 8 {
			line += "cpu" + itoa(i) + " 0 0 0 0 0 0 0 0\n"
		}
		return line
	}
	tree.write("stat", hostStat(100, 900))

	dir := t.TempDir()
	// All eight of the host's CPUs — what an unrestricted cgroup reports.
	cpuset := filepath.Join(dir, "cpuset.cpus.effective")
	if err := os.WriteFile(cpuset, []byte("0-7\n"), 0o644); err != nil {
		t.Fatalf("write cpuset: %v", err)
	}
	usage := filepath.Join(dir, "cpu.stat")
	writeUsage := func(micros int) {
		if err := os.WriteFile(usage, []byte("usage_usec "+itoa(micros)+"\n"), 0o644); err != nil {
			t.Fatalf("write cgroup usage: %v", err)
		}
	}
	writeUsage(0)
	quota := filepath.Join(dir, "cpu.max")
	if err := os.WriteFile(quota, []byte("max 100000\n"), 0o644); err != nil {
		t.Fatalf("write cgroup quota: %v", err)
	}

	if got := cgroupCPUSetCores([]string{cpuset}, 8); got != 0 {
		t.Fatalf("cgroupCPUSetCores() = %d for a cpuset holding every host CPU, want 0", got)
	}

	s := newTestSampler(t, tree, clock, Options{})
	s.cgroupCPUPaths = []cgroupCPUPath{{
		usage: usage, usageKey: cgroupCPUUsageKey, usageUnit: time.Microsecond, quota: quota,
	}}
	s.cgroupCPUSetPaths = []string{cpuset}
	s.sample(context.Background())

	// The host spends 750 of the interval's 1000 ticks busy; Silo uses 2 of its
	// 40 available core-seconds, which is 5%.
	writeUsage(2_000_000)
	tree.write("stat", hostStat(850, 1150))
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.CPUPct != 75 {
		t.Fatalf("CPUPct = %d, want the host's 75 — nothing caps this process", system.CPUPct)
	}
	if system.Cores != 8 {
		t.Fatalf("Cores = %d, want the host's 8", system.Cores)
	}
}

// A quota sized to the whole box is the ordinary way a deployment says "use this
// machine", and it restricts nothing. The boundary matters on its own: the
// over-capacity case is a misconfiguration, while quota == host cores is
// deliberate and common, and reading it as a cap moves the measurement into the
// cgroup exactly where the machine is shared.
func TestCPUQuotaEqualToTheHostReportsTheHost(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	hostStat := func(busy, idle int) string {
		line := "cpu  " + itoa(busy) + " 0 0 " + itoa(idle) + " 0 0 0 0\n"
		for i := range 4 {
			line += "cpu" + itoa(i) + " 0 0 0 0 0 0 0 0\n"
		}
		return line
	}
	tree.write("stat", hostStat(100, 900))

	dir := t.TempDir()
	usage := filepath.Join(dir, "cpu.stat")
	writeUsage := func(micros int) {
		if err := os.WriteFile(usage, []byte("usage_usec "+itoa(micros)+"\n"), 0o644); err != nil {
			t.Fatalf("write cgroup usage: %v", err)
		}
	}
	writeUsage(0)
	quota := filepath.Join(dir, "cpu.max")
	// Exactly the host's four cores.
	if err := os.WriteFile(quota, []byte("400000 100000\n"), 0o644); err != nil {
		t.Fatalf("write cgroup quota: %v", err)
	}

	s := newTestSampler(t, tree, clock, Options{})
	s.cgroupCPUPaths = []cgroupCPUPath{{
		usage: usage, usageKey: cgroupCPUUsageKey, usageUnit: time.Microsecond, quota: quota,
	}}
	s.sample(context.Background())

	// Silo is nearly idle; the machine it shares is three quarters busy.
	writeUsage(200_000)
	tree.write("stat", hostStat(850, 1150))
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	system := s.Snapshot().System
	if system.CPUPct != 75 {
		t.Fatalf("CPUPct = %d, want the host's 75 — a whole-machine quota caps nothing", system.CPUPct)
	}
	if system.Cores != 4 {
		t.Fatalf("Cores = %d, want the host's 4", system.Cores)
	}
}

// The rule is one predicate, and it is the boundary that carries it: strictly
// smaller than the machine binds, as large as the machine does not, and an
// unknown machine is no reason to discard a cap.
func TestCgroupCapBindsOnlyBelowTheHostSize(t *testing.T) {
	for _, test := range []struct {
		name      string
		cores     float64
		hostCores int
		want      bool
	}{
		{name: "half the host", cores: 2, hostCores: 4, want: true},
		{name: "just under the host", cores: 3.5, hostCores: 4, want: true},
		{name: "the whole host", cores: 4, hostCores: 4, want: false},
		{name: "more than the host", cores: 8, hostCores: 4, want: false},
		{name: "no cap", cores: 0, hostCores: 4, want: false},
		{name: "unknown host keeps a cap", cores: 2, hostCores: 0, want: true},
		{name: "unknown host still has no cap to keep", cores: 0, hostCores: 0, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := cgroupCapBinds(test.cores, test.hostCores); got != test.want {
				t.Fatalf("cgroupCapBinds(%v, %d) = %v, want %v", test.cores, test.hostCores, got, test.want)
			}
		})
	}
}

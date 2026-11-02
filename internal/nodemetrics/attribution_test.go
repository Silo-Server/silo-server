package nodemetrics

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestResourceAttributionRejectsUnmeasuredIdle(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	s := newTestSampler(t, tree, clock, Options{})
	s.sample(context.Background())
	a := s.Snapshot().Attribution
	if a == nil || a.CPU.Available || a.Memory.Available || a.Network.Available || a.Load.Available {
		t.Fatalf("missing procfs files produced available sources: %+v", a)
	}
	if a.Process.ResidentBytes != nil || a.Process.ReadBytes != nil || a.Process.HeapLiveBytes == nil || a.Children != nil {
		t.Fatalf("OS unavailability affected runtime measurements or fabricated OS readings: %+v", a)
	}
	values := gatherNames(t, s)
	for _, name := range []string{"streamapp_node_cpu_percent", "streamapp_node_memory_used_bytes", "streamapp_node_load1", "streamapp_node_network_tx_bps", "silo_process_io_read_bytes_total"} {
		if _, ok := values[name]; ok {
			t.Fatalf("missing source emitted %s", name)
		}
	}
}

func TestResourceAttributionCgroupMemoryAncestorAndFailure(t *testing.T) {
	tree := newProcTree(t)
	tree.write("meminfo", "MemTotal: 8192 kB\nMemAvailable: 4096 kB\n")
	tree.write("cgroup/leaf/memory.max", "max\n")
	tree.write("cgroup/leaf/memory.current", "1000\n")
	tree.write("cgroup/memory.max", "4096\n")
	tree.write("cgroup/memory.current", "3072\n")
	tree.write("cgroup/memory.stat", "inactive_file 1024\n")
	tree.write("cgroup/memory.events", "low 0\nhigh 3\nmax 2\noom 1\noom_kill 1\n")
	tree.write("cgroup/memory.pressure", "some avg10=12.5 avg60=0 avg300=0 total=55\nfull avg10=1.25 avg60=0 avg300=0 total=4\n")
	level := func(dir string) cgroupUsagePath {
		return cgroupUsagePath{limit: filepath.Join(tree.root, dir, "memory.max"), usage: filepath.Join(tree.root, dir, "memory.current"), stat: filepath.Join(tree.root, dir, "memory.stat"), inactiveFile: cgroupInactiveFileKeyV2}
	}
	s := newTestSampler(t, tree, newFakeClock(), Options{})
	s.cgroupUsagePaths = []cgroupUsagePath{level("cgroup/leaf"), level("cgroup")}
	s.sample(context.Background())
	a := s.Snapshot().Attribution
	c := a.CgroupMemory
	if a.Memory.Scope != "cgroup" || !a.Memory.Available || c.Scope != "ancestor" || *c.CurrentBytes != 3072 || *c.WorkingSetBytes != 2048 || *c.LimitBytes != 4096 || *c.OOMKills != 1 || *c.PressureSomePct != 12.5 || *c.PressureFullPct != 1.25 {
		t.Fatalf("incorrect binding scope/readings: %+v %+v", a.Memory, c)
	}
	if err := os.Remove(filepath.Join(tree.root, "cgroup/memory.current")); err != nil {
		t.Fatal(err)
	}
	s.sample(context.Background())
	a = s.Snapshot().Attribution
	if a.Memory.Available || a.CgroupMemory.CurrentBytes != nil || a.CgroupMemory.WorkingSetBytes != nil {
		t.Fatalf("missing cgroup usage read as host memory or zero: %+v", a)
	}
}

func TestResourceAttributionCgroupCPUVersions(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			tree := newProcTree(t)
			path := cgroupCPUPath{quota: filepath.Join(tree.root, "cpu.max")}
			if version == "v1" {
				tree.write("cpu.stat", "nr_periods 20\nnr_throttled 4\nthrottled_time 250000000\n")
			} else {
				path.usageKey = "usage_usec"
				tree.write("cpu.stat", "usage_usec 500000\nnr_periods 20\nnr_throttled 4\nthrottled_usec 250000\n")
			}
			got := sampleCgroupCPU(path, path, 0.5, 500000000)
			if got.Version != version || *got.QuotaCores != 0.5 || *got.UsageSeconds != 0.5 || *got.ThrottledSeconds != 0.25 || *got.Periods != 20 || *got.ThrottledPeriods != 4 || got.PressureSomePct != nil {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func procStatFixture(pid, parent int, comm string, start uint64) string {
	fields := make([]string, 52)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[1], fields[2], fields[3] = strconv.Itoa(pid), "("+comm+")", "S", strconv.Itoa(parent)
	fields[13], fields[14], fields[19], fields[21], fields[22], fields[23] = "150", "50", "4", strconv.FormatUint(start, 10), "10485760", "512"
	return strings.Join(fields, " ") + "\n"
}

func TestResourceAttributionChildrenValidateOwnershipAndCap(t *testing.T) {
	tree := newProcTree(t)
	tree.write("12/stat", procStatFixture(12, 10, "ffmpeg worker", 100))
	tree.write("13/stat", procStatFixture(13, 99, "ffmpeg", 101))
	tree.write("14/stat", procStatFixture(14, 10, "unrelated", 102))
	got := sampleChildren(tree.root, 10, []int{12, 12, 13, 14, 15})
	if got.Sampled != 1 || got.Unavailable != 3 || got.CPUSeconds != 2 || got.ResidentBytes != 512*uint64(os.Getpagesize()) {
		t.Fatalf("wrong owned-child accounting: %+v", got)
	}
	pids := make([]int, maxSampledChildren+1)
	got = sampleChildren(tree.root, 10, pids)
	if !got.Truncated {
		t.Fatal("unbounded child sample")
	}
}

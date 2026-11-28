package nodemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
	return procStatTicksFixture(pid, parent, comm, start, 150, 50)
}

// procStatTicksFixture is a /proc/PID/stat line with the given user and system
// CPU ticks.
func procStatTicksFixture(pid, parent int, comm string, start, utime, stime uint64) string {
	fields := make([]string, 52)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[1], fields[2], fields[3] = strconv.Itoa(pid), "("+comm+")", "S", strconv.Itoa(parent)
	fields[13], fields[14], fields[19], fields[21], fields[22], fields[23] = strconv.FormatUint(utime, 10), strconv.FormatUint(stime, 10), "4", strconv.FormatUint(start, 10), "10485760", "512"
	return strings.Join(fields, " ") + "\n"
}

func TestResourceAttributionChildrenValidateOwnershipAndCap(t *testing.T) {
	tree := newProcTree(t)
	tree.write("12/stat", procStatFixture(12, 10, "ffmpeg worker", 100))
	tree.write("13/stat", procStatFixture(13, 99, "ffmpeg", 101))
	tree.write("14/stat", procStatFixture(14, 10, "unrelated", 102))
	got, _ := sampleChildren(tree.root, 10, []int{12, 12, 13, 14, 15})
	if got.Sampled != 1 || got.Unavailable != 3 || got.CPUSeconds != 2 || got.ResidentBytes != 512*uint64(os.Getpagesize()) {
		t.Fatalf("wrong owned-child accounting: %+v", got)
	}
	pids := make([]int, maxSampledChildren+1)
	got, _ = sampleChildren(tree.root, 10, pids)
	if !got.Truncated {
		t.Fatal("unbounded child sample")
	}
}

// testUserHZ is USER_HZ, the unit of the fixtures' CPU ticks and jiffies.
const testUserHZ = 100

// cpuPctView is the published snapshot as a monitor reads it on the wire.
type cpuPctView struct {
	System struct {
		CPUPct int `json:"cpu_pct"`
	} `json:"system"`
	Attribution struct {
		CPU     ResourceSource `json:"cpu"`
		Process struct {
			CPUPct *float64 `json:"cpu_pct"`
		} `json:"process"`
		Children struct {
			Unavailable int      `json:"unavailable"`
			Truncated   bool     `json:"truncated"`
			CPUPct      *float64 `json:"cpu_pct"`
		} `json:"children"`
	} `json:"attribution"`
	GPU []struct {
		VideoBusyPct *int `json:"video_busy_pct"`
	} `json:"gpu"`
}

// cpuPctHarness drives the sampler over a fake /proc in which the test sets the
// host's jiffies, this process's CPU ticks, its FFmpeg children and the boot
// clock before each five-second pass.
type cpuPctHarness struct {
	t        *testing.T
	tree     *procTree
	clock    *fakeClock
	s        *Sampler
	pid      int
	cores    int
	idle     uint64 // host idle jiffies, advanced by a full window each pass
	uptime   uint64 // seconds since boot
	children []int  // fixture child IDs; childPID gives each one's PID
}

// fakeChildPIDBase lifts fixture child PIDs above Linux's PID_MAX_LIMIT, so a
// fake child can never share a /proc entry with this test process.
const fakeChildPIDBase = 1 << 22

func childPID(id int) int { return fakeChildPIDBase + id }

// listed is the PIDs child discovery reports: h.children's.
func (h *cpuPctHarness) listed() []int {
	pids := make([]int, len(h.children))
	for i, id := range h.children {
		pids[i] = childPID(id)
	}
	return pids
}

func newCPUPctHarness(t *testing.T, cores int) *cpuPctHarness {
	h := &cpuPctHarness{t: t, tree: newProcTree(t), clock: newFakeClock(), pid: os.Getpid(), cores: cores, idle: 1000, uptime: 1000}
	h.tree.write("loadavg", "0 0 0 0/0 0\n")
	h.tree.write("meminfo", "MemTotal: 1024 kB\nMemAvailable: 512 kB\n")
	h.tree.write("net/dev", "")
	h.s = newTestSampler(t, h.tree, h.clock, Options{FFmpegChildren: h.listed})
	h.s.bootTicks = func() (uint64, bool) { return h.uptimeTicks(), true }
	return h
}

func (h *cpuPctHarness) uptimeTicks() uint64 { return h.uptime * testUserHZ }

func (h *cpuPctHarness) setProcess(ticks uint64) {
	h.tree.write(fmt.Sprintf("%d/stat", h.pid), procStatTicksFixture(h.pid, 1, "silo", 1, ticks-ticks/4, ticks/4))
}

func (h *cpuPctHarness) setChild(id int, start, ticks uint64) {
	pid := childPID(id)
	h.tree.write(fmt.Sprintf("%d/stat", pid), procStatTicksFixture(pid, h.pid, "ffmpeg", start, ticks-ticks/4, ticks/4))
}

func (h *cpuPctHarness) removeChild(id int) {
	if err := os.RemoveAll(filepath.Join(h.tree.root, strconv.Itoa(childPID(id)))); err != nil {
		h.t.Fatal(err)
	}
}

// pass advances five seconds of a fully idle host, samples once, and returns
// the published snapshot's wire form.
func (h *cpuPctHarness) pass() cpuPctView {
	h.t.Helper()
	h.clock.advance(5 * time.Second)
	h.uptime += 5
	h.idle += uint64(h.cores) * 5 * testUserHZ
	var stat strings.Builder
	fmt.Fprintf(&stat, "cpu  0 0 0 %d 0 0 0 0\n", h.idle)
	for i := range h.cores {
		fmt.Fprintf(&stat, "cpu%d 0 0 0 0 0 0 0 0\n", i)
	}
	h.tree.write("stat", stat.String())
	h.s.sample(context.Background())
	raw, err := json.Marshal(h.s.Snapshot())
	if err != nil {
		h.t.Fatal(err)
	}
	// An unmeasured share leaves its key out; null would decode the same.
	if strings.Contains(string(raw), `"cpu_pct":null`) {
		h.t.Fatalf("an unmeasured share is sent as null: %s", raw)
	}
	var view cpuPctView
	if err := json.Unmarshal(raw, &view); err != nil {
		h.t.Fatal(err)
	}
	return view
}

func wantPct(t *testing.T, label string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: omitted, want %.2f", label, want)
	}
	if *got != want {
		t.Fatalf("%s = %v, want %.2f", label, *got, want)
	}
}

func wantOmitted(t *testing.T, label string, got *float64) {
	t.Helper()
	if got != nil {
		t.Fatalf("%s = %v, want omitted", label, *got)
	}
}

// The process and child CPU shares are measured over the sample window against
// the capacity System.CPUPct used, so a monitor can chart them beside it.
func TestProcessAndChildCPUPercent(t *testing.T) {
	t.Run("host", func(t *testing.T) {
		h := newCPUPctHarness(t, 4)
		h.setProcess(1000)
		h.children = []int{200}
		h.setChild(200, 1000, 500)

		// No previous reading on the first pass, and on the second the host
		// counters only now have a pair: neither share has a window yet.
		a := h.pass()
		wantOmitted(t, "pass 1 process", a.Attribution.Process.CPUPct)
		wantOmitted(t, "pass 1 children", a.Attribution.Children.CPUPct)
		h.children = []int{200, 202}
		h.setChild(202, 2000, 5000) // long-lived
		a = h.pass()
		wantOmitted(t, "pass 2 process", a.Attribution.Process.CPUPct)
		wantOmitted(t, "pass 2 children", a.Attribution.Children.CPUPct)

		// 4 cores for 5 s is 2000 jiffies; 100 process ticks is 5%. Child 200
		// grows by 200; child 201 was born 3 s ago and is credited its lifetime
		// 50; child 202 is unreadable this pass.
		h.setProcess(1100)
		h.setChild(200, 1000, 700)
		h.setChild(201, h.uptimeTicks()+5*testUserHZ-300, 50)
		h.removeChild(202)
		h.children = []int{200, 201, 202}
		a = h.pass()
		wantPct(t, "pass 3 process", a.Attribution.Process.CPUPct, 5)
		wantPct(t, "pass 3 children", a.Attribution.Children.CPUPct, 12.5)

		// PID 200 now belongs to a new child born 1 s ago (20 ticks, credited
		// in full rather than read as a counter running backwards), 201 grows
		// by 100, and long-lived 202 returns with a large lifetime that is
		// mostly earlier windows' work: it becomes a baseline, not a spike.
		// An idle process measures 0, not omitted.
		h.setChild(200, h.uptimeTicks()+5*testUserHZ-100, 20)
		h.setChild(201, h.uptimeTicks()-300, 150)
		h.setChild(202, 2000, 9000)
		a = h.pass()
		wantPct(t, "pass 4 process", a.Attribution.Process.CPUPct, 0)
		wantPct(t, "pass 4 children", a.Attribution.Children.CPUPct, 6)

		// 201 exits and contributes nothing; 200 grows 50 and 202 grows 100.
		h.removeChild(201)
		h.setChild(200, h.uptimeTicks()-100, 70)
		h.setChild(202, 2000, 9100)
		h.children = []int{200, 202}
		a = h.pass()
		wantPct(t, "pass 5 children", a.Attribution.Children.CPUPct, 7.5)

		// A truncated pass did not see every child: no child share, and no
		// baseline to measure the next pass against. The process is unaffected.
		h.children = append([]int{200, 202}, make([]int, maxSampledChildren)...)
		a = h.pass()
		if !a.Attribution.Children.Truncated {
			t.Fatal("pass 6 was not truncated")
		}
		wantOmitted(t, "pass 6 children", a.Attribution.Children.CPUPct)
		wantPct(t, "pass 6 process", a.Attribution.Process.CPUPct, 0)
		h.children = []int{200, 202}
		a = h.pass()
		wantOmitted(t, "pass 7 children", a.Attribution.Children.CPUPct)
		h.setChild(202, 2000, 9200)
		a = h.pass()
		wantPct(t, "pass 8 children", a.Attribution.Children.CPUPct, 5)

		// A child whose start time says it was born inside the window but whose
		// 5000-tick lifetime exceeds the window's 2000 ticks of capacity cannot
		// have earned it there: a baseline, not a spike.
		h.setChild(202, 2000, 9300)
		h.setChild(203, h.uptimeTicks()+5*testUserHZ-100, 5000)
		h.children = []int{200, 202, 203}
		a = h.pass()
		wantPct(t, "pass 9 children", a.Attribution.Children.CPUPct, 5)

		// The host's capacity changed, so this window's share would mix two
		// denominators: both are omitted, and the next pass only re-baselines.
		h.cores = 2
		h.setProcess(1200)
		a = h.pass()
		wantOmitted(t, "capacity change process", a.Attribution.Process.CPUPct)
		wantOmitted(t, "capacity change children", a.Attribution.Children.CPUPct)
		a = h.pass()
		wantOmitted(t, "after capacity change process", a.Attribution.Process.CPUPct)
		h.setProcess(1300)
		a = h.pass()
		wantPct(t, "2 cores process", a.Attribution.Process.CPUPct, 10)

		// A process counter that ran backwards is a reset, not an idle window.
		h.setProcess(1250)
		a = h.pass()
		wantOmitted(t, "process counter reset", a.Attribution.Process.CPUPct)
	})

	t.Run("cgroup quota", func(t *testing.T) {
		h := newCPUPctHarness(t, 4)
		cg := filepath.Join(h.tree.root, "cgroup")
		h.tree.write("cgroup/cpu.max", "200000 100000\n")
		h.s.cgroupCPUPaths = []cgroupCPUPath{{usage: filepath.Join(cg, "cpu.stat"), usageKey: cgroupCPUUsageKey, usageUnit: time.Microsecond, quota: filepath.Join(cg, "cpu.max")}}
		usage := uint64(1_000_000)
		cgroupPass := func() cpuPctView {
			usage += 5_000_000 // half the two-core quota
			h.tree.write("cgroup/cpu.stat", fmt.Sprintf("usage_usec %d\n", usage))
			return h.pass()
		}
		h.setProcess(1000)
		cgroupPass()
		a := cgroupPass()
		wantOmitted(t, "pass 2 process", a.Attribution.Process.CPUPct)
		// 100 ticks is 1 s of CPU over 5 s of a two-core quota: 10%, where the
		// host's four cores would have made it 5%.
		h.setProcess(1100)
		a = cgroupPass()
		if a.Attribution.CPU.Scope != scopeCgroup || a.System.CPUPct != 50 {
			t.Fatalf("cgroup did not bind: %+v cpu_pct=%d", a.Attribution.CPU, a.System.CPUPct)
		}
		wantPct(t, "cgroup process", a.Attribution.Process.CPUPct, 10)
		wantPct(t, "cgroup children", a.Attribution.Children.CPUPct, 0)

		// CFS enforces the quota per 100 ms period, so a child born just inside
		// the window can use a little more than quota times the window: 1005
		// ticks against 1000. It is still credited, not mistaken for earlier
		// work, because no CPU the machine has could have run it for longer.
		h.children = []int{300}
		h.setChild(300, h.uptimeTicks()+5*testUserHZ-499, 1005)
		a = cgroupPass()
		wantPct(t, "cgroup child above quota", a.Attribution.Children.CPUPct, 100)
	})
}

// The disk pass between the /proc/stat read and the attribution read can take
// a different time each pass (it queries the media roots). The shares are
// measured over their own window, so a process using a steady 20 ticks a
// second on 4 cores reads 5% whatever the delay.
func TestCPUShareWindowIgnoresDiskPassDelay(t *testing.T) {
	h := newCPUPctHarness(t, 4)
	start := h.clock.now()
	delays := []time.Duration{0, 0, 0, 2 * time.Second, 0, 0}
	pass := 0
	h.s.mediaRoots = func(context.Context) []string {
		h.clock.advance(delays[pass])
		pass++
		h.setProcess(uint64(h.clock.now().Sub(start).Seconds() * 20))
		return nil
	}
	h.setProcess(0)
	for i := range delays {
		a := h.pass()
		h.clock.advance(-delays[i]) // keep /proc/stat reads five seconds apart
		if i >= 2 {
			wantPct(t, fmt.Sprintf("pass %d process", i+1), a.Attribution.Process.CPUPct, 5)
		}
	}
}

// Child discovery reads the kernel's per-task children lists, which can miss a
// live child for a pass, and takes time of its own. Neither may change what a
// child is charged.
func TestChildCPUShareSurvivesDiscoveryGaps(t *testing.T) {
	h := newCPUPctHarness(t, 4)
	var bootExtra uint64
	h.s.bootTicks = func() (uint64, bool) { return h.uptimeTicks() + bootExtra, true }
	listed := true
	h.s.ffmpegPIDs = func() []int {
		bootExtra = testUserHZ // discovery takes a second
		if !listed {
			return nil
		}
		return h.listed()
	}
	h.setProcess(1000)
	h.children = []int{200}
	ticks := uint64(1000)
	h.setChild(200, 1, ticks)
	pass := func() cpuPctView {
		bootExtra = 0
		ticks += 200 // 200 ticks of 2000 every window: 10%
		h.setChild(200, 1, ticks)
		return h.pass()
	}
	pass()
	pass()
	wantPct(t, "listed", pass().Attribution.Children.CPUPct, 10)

	// Discovery misses the child for a pass, then lists it again.
	listed = false
	wantPct(t, "missed by discovery", pass().Attribution.Children.CPUPct, 10)
	listed = true
	wantPct(t, "listed again", pass().Attribution.Children.CPUPct, 10)

	// A child born half a second after the window opened (which, with a
	// one-second discovery before each boundary, was a second past the
	// previous pass's start) is credited its lifetime.
	h.children = []int{200, 201}
	h.setChild(201, h.uptimeTicks()+testUserHZ+testUserHZ/2, 100)
	wantPct(t, "born inside the window", pass().Attribution.Children.CPUPct, 15)

	// A child that started in the opening reading's own tick may have started
	// before it, so it becomes a baseline too.
	h.children = []int{200, 201, 203}
	start203 := h.uptimeTicks() + testUserHZ
	h.setChild(203, start203, 40)
	wantPct(t, "born in the opening tick", pass().Attribution.Children.CPUPct, 10)

	// A child born after the window closed is not charged to that window; it
	// becomes a baseline.
	h.children = []int{200, 201, 202}
	h.setChild(202, h.uptimeTicks()+6*testUserHZ+testUserHZ/2, 50)
	wantPct(t, "born after the window closed", pass().Attribution.Children.CPUPct, 10)

	// 201 and 202 exit, and the kernel hands PID 202 to an ffprobe. It is not
	// the baseline child, so it is not read as an unreadable one.
	h.children = []int{200}
	h.removeChild(201)
	h.tree.write(fmt.Sprintf("%d/stat", childPID(202)), procStatTicksFixture(childPID(202), h.pid, "ffprobe", h.uptimeTicks()+testUserHZ, 1, 0))
	if a := pass(); a.Attribution.Children.Unavailable != 0 {
		t.Fatalf("reused PID counted as %d unavailable children", a.Attribution.Children.Unavailable)
	}

	// Baseline child 203 renames itself, keeping its lifetime, and discovery
	// no longer lists it. It is no longer an FFmpeg child, so it is not read
	// as an unreadable one either.
	h.tree.write(fmt.Sprintf("%d/stat", childPID(203)), procStatTicksFixture(childPID(203), h.pid, "worker", start203, 40, 0))
	if a := pass(); a.Attribution.Children.Unavailable != 0 {
		t.Fatalf("renamed child counted as %d unavailable children", a.Attribution.Children.Unavailable)
	}
}

// Listing children takes a varying time. The window spans the counter reads,
// not the listing, so a child using a steady 50 ticks a second on 4 cores
// reads 12.5% whatever the delay.
func TestChildCPUShareWindowIgnoresDiscoveryDelay(t *testing.T) {
	h := newCPUPctHarness(t, 4)
	start := h.clock.now()
	delays := []time.Duration{0, 0, 0, time.Second, 0, 0}
	var pending time.Duration // the listing's delay, once per pass (GPU sampling lists children too)
	h.children = []int{200}
	h.s.ffmpegPIDs = func() []int {
		h.clock.advance(pending)
		pending = 0
		h.setChild(200, 1, uint64(h.clock.now().Sub(start).Seconds()*50))
		return h.listed()
	}
	h.setProcess(1000)
	for i := range delays {
		pending = delays[i]
		a := h.pass()
		h.clock.advance(-delays[i]) // keep passes five seconds apart
		if i >= 2 {
			wantPct(t, fmt.Sprintf("pass %d children", i+1), a.Attribution.Children.CPUPct, 12.5)
		}
	}
}

// GPU engine counters keep running while the system and attribution passes
// before them take a varying time, so GPU busyness is timed at the counter
// reads: an FFmpeg child keeping the video engine busy half the time reads 50%
// whatever the delay.
func TestGPUBusyWindowIgnoresEarlierPassDelay(t *testing.T) {
	h := newCPUPctHarness(t, 4)
	start := h.clock.now()
	delays := []time.Duration{0, 0, 0, time.Second, 0, 0}
	var pending time.Duration // the attribution listing's delay; GPU sampling lists next
	h.children = []int{200}
	h.setChild(200, 1, 100)
	h.s.ffmpegPIDs = func() []int {
		h.clock.advance(pending)
		pending = 0
		busyNS := h.clock.now().Sub(start).Nanoseconds() / 2
		h.tree.write(fmt.Sprintf("%d/fdinfo/5", childPID(200)), fmt.Sprintf("drm-pdev:\t0000:00:02.0\ndrm-client-id:\t7\ndrm-engine-video:\t%d ns\n", busyNS))
		return h.listed()
	}
	h.setProcess(1000)
	for i := range delays {
		pending = delays[i]
		a := h.pass()
		h.clock.advance(-delays[i]) // keep passes five seconds apart
		if i < 1 {
			continue
		}
		if len(a.GPU) != 1 || a.GPU[0].VideoBusyPct == nil || *a.GPU[0].VideoBusyPct != 50 {
			t.Fatalf("pass %d gpu = %+v, want video 50%%", i+1, a.GPU)
		}
	}
}

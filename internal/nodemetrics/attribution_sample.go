package nodemetrics

import (
	"math"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/procfs"
)

// procfsUserHZ is the kernel's USER_HZ, the unit of /proc/PID/stat CPU times
// and of /proc/stat jiffies. Linux fixes it at 100 on every architecture Silo
// ships for, and procfs assumes the same value for CPUTime.
const procfsUserHZ = 100

// cpuShareBasis is the capacity one pass's System.CPUPct measured against, in
// cores: the host's (or lxcfs view's) CPU count, or a binding cgroup quota.
// Shares divide CPU used over the attribution window by that many cores over
// the same window, so they stay on System.CPUPct's scale without borrowing
// System.CPUPct's own window, which the disk pass between the two reads can
// stretch or shrink.
type cpuShareBasis struct {
	source    string  // ResourceSource.Source: procfs, lxcfs, cgroup_v1, cgroup_v2
	capacity  float64 // cores
	available bool    // System.CPUPct was measured this pass
}

// capacityTicks is the CPU, in USER_HZ ticks, that capacity can supply over
// elapsed.
func (b cpuShareBasis) capacityTicks(elapsed time.Duration) float64 {
	return b.capacity * elapsed.Seconds() * procfsUserHZ
}

// childKey identifies one process lifetime: a PID the kernel reuses arrives
// with a different start time, so it is never mistaken for its predecessor.
type childKey struct {
	pid   int
	start uint64
}

func (s *Sampler) procSource(name string, available bool) ResourceSource {
	if s.procDirFor(name) != s.procDir {
		return ResourceSource{Scope: "virtualized_host", Source: "lxcfs", Available: available}
	}
	return ResourceSource{Scope: "host", Source: "procfs", Available: available}
}

func (s *Sampler) sampleAttribution() *ResourceAttribution {
	out := &ResourceAttribution{
		InstanceID: s.instanceID, SampleIntervalSeconds: s.interval.Seconds(),
		CPU: s.cpuSource, Memory: s.memorySource,
		Load:      s.loadSource,
		Network:   ResourceSource{Scope: "network_namespace", Source: "procfs", Available: s.networkAvailable},
		CgroupCPU: s.cgroupCPUDetails, CgroupMemory: s.cgroupMemoryDetails,
		DroppedDiskRoots: s.droppedRoots,
	}
	// The window end is taken right before the reads it closes.
	at := s.now()
	var procTicks uint64
	var childTicks map[childKey]uint64
	out.Process, procTicks = sampleProcess(s.procDir, os.Getpid())
	if out.Process.CPUSeconds != nil {
		out.Children, childTicks = sampleChildren(s.procDir, os.Getpid(), s.ffmpegPIDs())
	}
	s.attributeCPU(out, at, procTicks, childTicks)
	out.Disks = s.diskDetails
	return out
}

// attributeCPU fills the process and child CPU shares for the window since the
// previous baseline and records this pass as the next baseline.
//
// Both shares use the denominator System.CPUPct used this pass, so they stay
// comparable to it. Any pass whose denominator cannot be trusted, or whose
// process counter cannot be read, publishes neither share and discards every
// baseline; the pass after it records fresh baselines and publishes nothing, so
// no share ever spans a gap or a change of capacity.
func (s *Sampler) attributeCPU(out *ResourceAttribution, at time.Time, procTicks uint64, children map[childKey]uint64) {
	basis, previous := s.cpuShare, s.prevCPUShare
	s.prevCPUShare = basis
	elapsed := at.Sub(s.prevAttrAt)
	if out.Process.CPUSeconds == nil || !basis.available || basis.source != previous.source || basis.capacity != previous.capacity ||
		(!s.prevAttrAt.IsZero() && elapsed <= 0) {
		s.prevAttrAt, s.prevProcTicks, s.prevChildTicks = time.Time{}, 0, nil
		return
	}
	if !s.prevAttrAt.IsZero() {
		s.publishCPUShares(out, basis, elapsed, procTicks, children)
	}
	s.prevAttrAt, s.prevProcTicks = at, procTicks
	// A truncated pass did not see every child, so its map is not a baseline:
	// a child it skipped would read as new next pass. sampleChildren returns a
	// non-nil map whenever it returns stats, so an empty one is still a baseline.
	s.prevChildTicks = nil
	if out.Children != nil && !out.Children.Truncated {
		s.prevChildTicks = children
	}
}

// publishCPUShares sets the shares for a window that has a usable baseline.
// Children need their own: a truncated pass leaves none behind.
func (s *Sampler) publishCPUShares(out *ResourceAttribution, basis cpuShareBasis, elapsed time.Duration, procTicks uint64, children map[childKey]uint64) {
	if pct, ok := cpuSharePercent(tickDelta(procTicks, s.prevProcTicks), basis, elapsed); ok {
		out.Process.CPUPct = new(pct)
	}
	if out.Children == nil || out.Children.Truncated || s.prevChildTicks == nil {
		return
	}
	if pct, ok := cpuSharePercent(s.childTickDelta(children, basis, elapsed), basis, elapsed); ok {
		out.Children.CPUPct = new(pct)
	}
}

// childTickDelta totals the CPU ticks live children used in the window.
//
// A child in the baseline contributes its growth. A child the baseline has not
// seen contributes its whole lifetime only when it was born inside the window;
// an older one (a child that was unreadable for a pass, say) only becomes a
// baseline, because its lifetime is mostly earlier windows' work. A child that
// exited contributes nothing, so this is a lower bound.
//
// Birth is judged on the clock a start time is counted on: the kernel's boot
// clock, read directly because lxcfs replaces /proc/uptime with a container's
// own. A lifetime larger than the window's whole capacity cannot have been
// earned inside it either way, so it stays a baseline.
func (s *Sampler) childTickDelta(children map[childKey]uint64, basis cpuShareBasis, elapsed time.Duration) uint64 {
	boot, bootOK := s.bootTicks()
	windowTicks := elapsed.Seconds() * procfsUserHZ
	capacityTicks := basis.capacityTicks(elapsed)
	var total uint64
	for key, ticks := range children {
		if previous, seen := s.prevChildTicks[key]; seen {
			total += tickDelta(ticks, previous)
			continue
		}
		if bootOK && key.start <= boot && float64(boot-key.start) <= windowTicks+1 && float64(ticks) <= capacityTicks {
			total += ticks
		}
	}
	return total
}

func tickDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

// cpuSharePercent converts CPU ticks used over a window into a percentage of
// what basis's capacity could supply over it, rounded to 0.01 and clamped to
// 0-100.
func cpuSharePercent(ticks uint64, basis cpuShareBasis, elapsed time.Duration) (float64, bool) {
	capacity := basis.capacityTicks(elapsed)
	if capacity <= 0 {
		return 0, false
	}
	pct := float64(ticks) * 100 / capacity
	return math.Round(min(max(pct, 0), 100)*100) / 100, true
}

// sampleProcess reads this process's resource summary. The second result is
// its user plus system CPU in USER_HZ ticks, meaningful only when CPUSeconds is
// set.
func sampleProcess(procDir string, pid int) (*ProcessStats, uint64) {
	out := &ProcessStats{}
	// runtime/metrics reads a bounded selection without forcing a GC. It does
	// not expose a parallel runtime metric API: these are resource summaries.
	values := []metrics.Sample{{Name: "/gc/heap/live:bytes"}, {Name: "/memory/classes/total:bytes"}, {Name: "/sched/goroutines:goroutines"}}
	metrics.Read(values)
	for i, dst := range []**uint64{&out.HeapLiveBytes, &out.GoMemoryBytes, &out.Goroutines} {
		if values[i].Value.Kind() == metrics.KindUint64 {
			*dst = new(values[i].Value.Uint64())
		}
	}
	fs, err := procfs.NewFS(procDir)
	if err != nil {
		return out, 0
	}
	p, err := fs.Proc(pid)
	if err != nil {
		return out, 0
	}
	var ticks uint64
	if stat, err := p.Stat(); err == nil {
		ticks = uint64(stat.UTime) + uint64(stat.STime)
		out.CPUSeconds = new(stat.CPUTime())
		out.VirtualBytes = new(uint64(stat.VirtualMemory()))
		if resident := stat.ResidentMemory(); resident >= 0 {
			out.ResidentBytes = new(uint64(resident))
		}
		if stat.NumThreads >= 0 {
			out.Threads = new(uint64(stat.NumThreads))
		}
	}
	if count, err := p.FileDescriptorsLen(); err == nil {
		out.OpenFDs = new(uint64(count))
	}
	if limits, err := p.Limits(); err == nil && limits.OpenFiles < math.MaxUint64 {
		out.MaxFDs = new(limits.OpenFiles)
	}
	if io, err := p.IO(); err == nil {
		out.ReadBytes, out.WriteBytes = new(io.ReadBytes), new(io.WriteBytes)
	}
	return out, ticks
}

const maxSampledChildren = 256

// sampleChildren reads the owned FFmpeg children among pids. The map holds
// each validated child's user plus system CPU in USER_HZ ticks.
func sampleChildren(procDir string, parent int, pids []int) (*ChildStats, map[childKey]uint64) {
	out := &ChildStats{Workload: "ffmpeg"}
	fs, err := procfs.NewFS(procDir)
	if err != nil {
		return nil, nil
	}
	if len(pids) > maxSampledChildren {
		out.Truncated = true
		pids = pids[:maxSampledChildren]
	}
	seen := make(map[int]bool, len(pids))
	ticks := make(map[childKey]uint64, len(pids))
	for _, pid := range pids {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		p, err := fs.Proc(pid)
		if err != nil {
			out.Unavailable++
			continue
		}
		before, err := p.Stat()
		if err != nil || before.PPID != parent || !strings.Contains(strings.ToLower(before.Comm), "ffmpeg") {
			out.Unavailable++
			continue
		}
		// Validate lifetime and ownership a second time before publishing. A PID
		// reused while reading must not charge an unrelated process to Silo.
		after, err := p.Stat()
		if err != nil || after.Starttime != before.Starttime || after.PPID != parent || after.Comm != before.Comm || after.ResidentMemory() < 0 {
			out.Unavailable++
			continue
		}
		out.Sampled++
		out.ResidentBytes += uint64(after.ResidentMemory())
		out.CPUSeconds += after.CPUTime()
		ticks[childKey{pid: pid, start: after.Starttime}] = uint64(after.UTime) + uint64(after.STime)
	}
	return out, ticks
}

func sampleCgroupCPU(binding, leaf cgroupCPUPath, quota float64, usageNS int64) *CgroupCPUStats {
	out := &CgroupCPUStats{Version: "v1", Scope: "leaf", UsageSeconds: new(float64(usageNS) / 1e9)}
	if binding.usage != leaf.usage {
		out.Scope = scopeAncestor
	}
	if quota > 0 {
		out.QuotaCores = new(quota)
	}
	stat := filepath.Join(filepath.Dir(binding.quota), "cpu.stat")
	throttleKey, scale := "throttled_time", 1e9
	if binding.usageKey != "" {
		out.Version, throttleKey, scale = "v2", "throttled_usec", 1e6
	}
	out.Periods = cgroupCounter(stat, "nr_periods")
	out.ThrottledPeriods = cgroupCounter(stat, "nr_throttled")
	if value := cgroupCounter(stat, throttleKey); value != nil {
		out.ThrottledSeconds = new(float64(*value) / scale)
	}
	out.PressureSomePct, _ = readPressure(filepath.Join(filepath.Dir(binding.quota), "cpu.pressure"))
	return out
}

func sampleCgroupMemory(binding cgroupUsagePath, candidates []cgroupUsagePath) *CgroupMemoryStats {
	out := &CgroupMemoryStats{Version: "v1", Scope: "leaf"}
	if binding.inactiveFile == cgroupInactiveFileKeyV2 {
		out.Version = "v2"
	}
	for _, candidate := range candidates {
		if candidate.inactiveFile == binding.inactiveFile {
			if candidate.usage != binding.usage {
				out.Scope = scopeAncestor
			}
			break
		}
	}
	out.CurrentBytes = cgroupCounter(binding.usage, "")
	if limit, err := ReadCgroupMemoryLimit(binding.limit); err == nil {
		out.LimitBytes = new(uint64(limit))
	}
	if inactive := cgroupCounter(binding.stat, binding.inactiveFile); inactive != nil && out.CurrentBytes != nil && *inactive <= *out.CurrentBytes {
		out.WorkingSetBytes = new(*out.CurrentBytes - *inactive)
	}
	dir := filepath.Dir(binding.usage)
	if out.Version == "v2" {
		out.SwapBytes = cgroupCounter(filepath.Join(dir, "memory.swap.current"), "")
		events := filepath.Join(dir, "memory.events")
		out.HighEvents = cgroupCounter(events, "high")
		out.MaxEvents = cgroupCounter(events, "max")
		out.OOMEvents = cgroupCounter(events, "oom")
		out.OOMKills = cgroupCounter(events, "oom_kill")
		out.PressureSomePct, out.PressureFullPct = readPressure(filepath.Join(dir, "memory.pressure"))
	} else {
		out.MaxEvents = cgroupCounter(filepath.Join(dir, "memory.failcnt"), "")
		out.OOMKills = cgroupCounter(filepath.Join(dir, "memory.oom_control"), "oom_kill")
	}
	return out
}

func cgroupCounter(path, key string) *uint64 {
	var value int64
	var err error
	if key == "" {
		value, err = readCgroupSingleValue(path)
	} else {
		value, err = readCgroupStatKey(path, key)
	}
	if err != nil || value < 0 {
		return nil
	}
	return new(uint64(value))
}

// readPressure returns the kernel's 10-second pressure averages. Unsupported,
// disabled, malformed or denied sources remain absent, including NaN/Inf.
func readPressure(path string) (some, full *float64) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, field := range fields[1:] {
			key, rawValue, ok := strings.Cut(field, "=")
			if !ok || key != "avg10" {
				continue
			}
			value, err := strconv.ParseFloat(rawValue, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
				continue
			}
			switch fields[0] {
			case "some":
				some = new(value)
			case "full":
				full = new(value)
			}
		}
	}
	return some, full
}

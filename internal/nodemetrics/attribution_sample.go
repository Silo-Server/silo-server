package nodemetrics

import (
	"math"
	"os"
	"path/filepath"
	"runtime/metrics"
	"slices"
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
//
// machine is the CPUs a process can run on at all, which bounds what a new
// child can have used. A quota is not such a bound: CFS enforces it per period,
// and a window that overlaps one more period than it spans can hold more than
// quota times its length.
type cpuShareBasis struct {
	source    string  // ResourceSource.Source: procfs, lxcfs, cgroup_v1, cgroup_v2
	capacity  float64 // cores
	machine   float64 // cores
	available bool    // System.CPUPct was measured this pass
}

// coreTicks is the CPU, in USER_HZ ticks, that cores can supply over elapsed.
func coreTicks(cores float64, elapsed time.Duration) float64 {
	return cores * elapsed.Seconds() * procfsUserHZ
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
	// Children are listed first, because walking the kernel's children lists
	// takes a varying time. The window end is then taken on both clocks right
	// before the counter reads it closes, so every window spans the same
	// reads. The boot clock reading is the line a later child's start time
	// must cross to count as born inside the next window; a child born while
	// discovery ran, and missed by it, is counted from its first reading on.
	pids := s.childPIDs()
	at := s.now()
	boot, bootOK := s.bootTicks()
	var procTicks uint64
	var childTicks map[childKey]uint64
	out.Process, procTicks = sampleProcess(s.procDir, os.Getpid())
	if out.Process.CPUSeconds != nil {
		out.Children, childTicks = sampleChildren(s.procDir, os.Getpid(), pids)
	}
	s.attributeCPU(out, at, boot, bootOK, procTicks, childTicks)
	out.Disks = s.diskDetails
	return out
}

// childPIDs is the children to sample: the ones discovery lists, plus any
// child in the CPU baseline that discovery missed but that still exists. The
// kernel's children lists can miss a live child for a pass; without this its
// window would read as idle and its return would only re-baseline.
// sampleChildren still checks each one's parent, name and start time.
func (s *Sampler) childPIDs() []int {
	pids := s.ffmpegPIDs()
	if len(s.prevChildTicks) == 0 {
		return pids
	}
	fs, err := procfs.NewFS(s.procDir)
	if err != nil {
		return pids
	}
	pids = slices.Clip(pids) // appending must not write into the lister's array
	listed := make(map[int]bool, len(pids))
	for _, pid := range pids {
		listed[pid] = true
	}
	for key := range s.prevChildTicks {
		if listed[key.pid] {
			continue
		}
		// Only the same lifetime, still an owned FFmpeg: a PID the kernel
		// handed to another process, or a child that renamed itself, would
		// otherwise be counted as an unreadable child, and could push a full
		// listing over the cap.
		if p, err := fs.Proc(key.pid); err == nil {
			if stat, err := p.Stat(); err == nil && stat.Starttime == key.start && ownedFFmpeg(stat, os.Getpid()) {
				pids = append(pids, key.pid)
			}
		}
	}
	return pids
}

// attributeCPU fills the process and child CPU shares for the window since the
// previous baseline and records this pass as the next baseline.
//
// Both shares use the denominator System.CPUPct used this pass, so they stay
// comparable to it. Any pass whose denominator cannot be trusted, or whose
// process counter cannot be read, publishes neither share and discards every
// baseline; the pass after it records fresh baselines and publishes nothing, so
// no share ever spans a gap or a change of capacity.
func (s *Sampler) attributeCPU(out *ResourceAttribution, at time.Time, boot uint64, bootOK bool, procTicks uint64, children map[childKey]uint64) {
	basis, previous := s.cpuShare, s.prevCPUShare
	s.prevCPUShare = basis
	elapsed := at.Sub(s.prevAttrAt)
	if out.Process.CPUSeconds == nil || !basis.available || basis.source != previous.source || basis.capacity != previous.capacity ||
		(!s.prevAttrAt.IsZero() && elapsed <= 0) {
		s.prevAttrAt, s.prevProcTicks, s.prevChildTicks, s.prevBoot, s.prevBootOK = time.Time{}, 0, nil, 0, false
		return
	}
	if !s.prevAttrAt.IsZero() {
		closeBoot := boot
		if !bootOK {
			closeBoot = 0 // an unread clock credits no new child
		}
		s.publishCPUShares(out, basis, elapsed, closeBoot, procTicks, children)
	}
	s.prevAttrAt, s.prevProcTicks, s.prevBoot, s.prevBootOK = at, procTicks, boot, bootOK
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
func (s *Sampler) publishCPUShares(out *ResourceAttribution, basis cpuShareBasis, elapsed time.Duration, closeBoot, procTicks uint64, children map[childKey]uint64) {
	// A counter that ran backwards is a reset, not an idle window.
	if procTicks >= s.prevProcTicks {
		if pct, ok := cpuSharePercent(procTicks-s.prevProcTicks, basis, elapsed); ok {
			out.Process.CPUPct = new(pct)
		}
	}
	if out.Children == nil || out.Children.Truncated || s.prevChildTicks == nil {
		return
	}
	if pct, ok := cpuSharePercent(s.childTickDelta(children, basis, elapsed, closeBoot), basis, elapsed); ok {
		out.Children.CPUPct = new(pct)
	}
}

// childTickDelta totals the CPU ticks live children used in the window.
//
// A child in the baseline contributes its growth. A child the baseline has not
// seen contributes its whole lifetime only when it was born inside the window:
// its start time is after the boot clock reading taken when the window opened
// and before the one taken when it closed. Both clocks count whole ticks, so a
// child that started in the opening reading's own tick may have started before
// it and is left out. A child born after the close only becomes a baseline,
// and so does an older one (a child that was unreadable for a pass, say),
// because its lifetime is mostly earlier windows' work. A child that exited contributes nothing, so this is a lower bound.
//
// Start times count on the kernel's boot clock, which is read directly because
// lxcfs replaces /proc/uptime with a container's own. A lifetime larger than
// what every CPU it can run on could supply over the window cannot have been
// earned inside it either way, so it stays a baseline.
func (s *Sampler) childTickDelta(children map[childKey]uint64, basis cpuShareBasis, elapsed time.Duration, closeBoot uint64) uint64 {
	limitTicks := coreTicks(max(basis.capacity, basis.machine), elapsed)
	var total uint64
	for key, ticks := range children {
		if previous, seen := s.prevChildTicks[key]; seen {
			total += tickDelta(ticks, previous)
			continue
		}
		if s.prevBootOK && key.start > s.prevBoot && key.start < closeBoot && float64(ticks) <= limitTicks {
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
	capacity := coreTicks(basis.capacity, elapsed)
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
		if err != nil || !ownedFFmpeg(before, parent) {
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

// ownedFFmpeg reports whether stat is an FFmpeg process parent started.
func ownedFFmpeg(stat procfs.ProcStat, parent int) bool {
	return stat.PPID == parent && strings.Contains(strings.ToLower(stat.Comm), "ffmpeg")
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

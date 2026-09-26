package nodemetrics

import (
	"math"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strconv"
	"strings"

	"github.com/prometheus/procfs"
)

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
	out.Process = sampleProcess(s.procDir, os.Getpid())
	if out.Process.CPUSeconds != nil {
		out.Children = sampleChildren(s.procDir, os.Getpid(), s.ffmpegPIDs())
	}
	out.Disks = s.diskDetails
	return out
}

func sampleProcess(procDir string, pid int) *ProcessStats {
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
		return out
	}
	p, err := fs.Proc(pid)
	if err != nil {
		return out
	}
	if stat, err := p.Stat(); err == nil {
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
	return out
}

const maxSampledChildren = 256

func sampleChildren(procDir string, parent int, pids []int) *ChildStats {
	out := &ChildStats{Workload: "ffmpeg"}
	fs, err := procfs.NewFS(procDir)
	if err != nil {
		return nil
	}
	if len(pids) > maxSampledChildren {
		out.Truncated = true
		pids = pids[:maxSampledChildren]
	}
	seen := make(map[int]bool, len(pids))
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
	}
	return out
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

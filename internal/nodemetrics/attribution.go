package nodemetrics

import "time"

const (
	resourceCPU    = "cpu"
	resourceMemory = "memory"
	scopeCgroup    = "cgroup"
	scopeAncestor  = "ancestor"
)

// ResourceAttribution names the population behind each legacy system reading.
// Separate scopes overlap: never add process RSS, child RSS and cgroup memory.
// Missing optional values were not measured. InstanceID is random per sampler
// lifetime, not a hostname, and identifies a response behind a load balancer.
type ResourceAttribution struct {
	InstanceID            string             `json:"instance_id"`
	SampleIntervalSeconds float64            `json:"sample_interval_seconds"`
	SampleDurationSeconds float64            `json:"sample_duration_seconds"`
	CPU                   ResourceSource     `json:"cpu"`
	Memory                ResourceSource     `json:"memory"`
	Load                  ResourceSource     `json:"load"`
	Network               ResourceSource     `json:"network"`
	Process               *ProcessStats      `json:"process,omitempty"`
	CgroupCPU             *CgroupCPUStats    `json:"cgroup_cpu,omitempty"`
	CgroupMemory          *CgroupMemoryStats `json:"cgroup_memory,omitempty"`
	Children              *ChildStats        `json:"children,omitempty"`
	Disks                 []DiskDetails      `json:"disks,omitempty"`
	DroppedDiskRoots      int                `json:"dropped_disk_roots"`
}

type ResourceSource struct {
	Scope     string `json:"scope"`  // host, virtualized_host, cgroup, network_namespace
	Source    string `json:"source"` // procfs, lxcfs, cgroup_v1, cgroup_v2
	Available bool   `json:"available"`
}

// ProcessStats describes this Silo process. Linux I/O counters also include
// waited-for children; CPU and RSS describe the process itself. HeapLiveBytes is the last GC's
// marked live Go heap; RSS includes resident native allocations and mappings.
// Their difference is not an exact native allocation measurement.
type ProcessStats struct {
	ResidentBytes *uint64  `json:"resident_bytes,omitempty"`
	VirtualBytes  *uint64  `json:"virtual_bytes,omitempty"`
	CPUSeconds    *float64 `json:"cpu_seconds,omitempty"`
	Threads       *uint64  `json:"threads,omitempty"`
	OpenFDs       *uint64  `json:"open_fds,omitempty"`
	MaxFDs        *uint64  `json:"max_fds,omitempty"`
	// Linux /proc/PID/io includes I/O attributed to waited-for children.
	ReadBytes     *uint64 `json:"read_bytes,omitempty"`
	WriteBytes    *uint64 `json:"write_bytes,omitempty"`
	HeapLiveBytes *uint64 `json:"heap_live_bytes,omitempty"`
	GoMemoryBytes *uint64 `json:"go_memory_bytes,omitempty"`
	Goroutines    *uint64 `json:"goroutines,omitempty"`
}

// CgroupCPUStats pairs usage and limits from the same visible cgroup level.
// Ancestor limits outside the process's cgroup namespace are not observable.
type CgroupCPUStats struct {
	Version          string   `json:"version"`
	Scope            string   `json:"scope"` // leaf or ancestor
	QuotaCores       *float64 `json:"quota_cores,omitempty"`
	UsageSeconds     *float64 `json:"usage_seconds,omitempty"`
	ThrottledSeconds *float64 `json:"throttled_seconds,omitempty"`
	Periods          *uint64  `json:"periods,omitempty"`
	ThrottledPeriods *uint64  `json:"throttled_periods,omitempty"`
	PressureSomePct  *float64 `json:"pressure_some_pct,omitempty"`
}

type CgroupMemoryStats struct {
	Version         string   `json:"version"`
	Scope           string   `json:"scope"` // leaf or ancestor
	CurrentBytes    *uint64  `json:"current_bytes,omitempty"`
	LimitBytes      *uint64  `json:"limit_bytes,omitempty"`
	WorkingSetBytes *uint64  `json:"working_set_bytes,omitempty"`
	SwapBytes       *uint64  `json:"swap_bytes,omitempty"`
	HighEvents      *uint64  `json:"high_events,omitempty"`
	MaxEvents       *uint64  `json:"max_events,omitempty"`
	OOMEvents       *uint64  `json:"oom_events,omitempty"`
	OOMKills        *uint64  `json:"oom_kills,omitempty"`
	PressureSomePct *float64 `json:"pressure_some_pct,omitempty"`
	PressureFullPct *float64 `json:"pressure_full_pct,omitempty"`
}

// ChildStats is a bounded point-in-time view of owned FFmpeg processes. CPU and
// RSS are sums of live processes; CPU can decrease when a child exits. RSS can
// count shared pages more than once. Short-lived work requires owner lifecycle
// counters and is deliberately not represented as a cumulative total here.
type ChildStats struct {
	Workload      string  `json:"workload"`
	Sampled       int     `json:"sampled"`
	Unavailable   int     `json:"unavailable"`
	Truncated     bool    `json:"truncated"`
	ResidentBytes uint64  `json:"resident_bytes"`
	CPUSeconds    float64 `json:"cpu_seconds"`
}

// DiskDetails extends operational snapshots without adding fields to the frozen
// v1 SystemStats wire shape. Role matches System.Disks; values have its freshness.
type DiskDetails struct {
	Role        string  `json:"role"`
	InodesUsed  *uint64 `json:"inodes_used,omitempty"`
	InodesTotal *uint64 `json:"inodes_total,omitempty"`
}

// Stale reports a stalled sampler. Disk probes have their own stale flags.
func (s Snapshot) Stale(now time.Time) bool {
	interval := DefaultInterval
	if s.Attribution != nil && s.Attribution.SampleIntervalSeconds > 0 {
		interval = time.Duration(s.Attribution.SampleIntervalSeconds * float64(time.Second))
	}
	return s.SampledAt.IsZero() || now.Sub(s.SampledAt) > 3*interval
}

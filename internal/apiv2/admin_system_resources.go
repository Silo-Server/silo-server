package apiv2

// AdminResourceAttribution names the population behind each legacy system reading.
// Separate scopes overlap: never add process RSS, child RSS and cgroup memory.
// Missing optional values were not measured. InstanceID is random per sampler
// lifetime, not a hostname, and identifies a response behind a load balancer.
type AdminResourceAttribution struct {
	InstanceID            string                  `json:"instance_id"`
	SampleIntervalSeconds float64                 `json:"sample_interval_seconds"`
	SampleDurationSeconds float64                 `json:"sample_duration_seconds"`
	CPU                   AdminResourceSource     `json:"cpu"`
	Memory                AdminResourceSource     `json:"memory"`
	Load                  AdminResourceSource     `json:"load"`
	Network               AdminResourceSource     `json:"network"`
	Process               *AdminProcessStats      `json:"process,omitempty"`
	CgroupCPU             *AdminCgroupCPUStats    `json:"cgroup_cpu,omitempty"`
	CgroupMemory          *AdminCgroupMemoryStats `json:"cgroup_memory,omitempty"`
	Children              *AdminChildStats        `json:"children,omitempty"`
	Disks                 []AdminDiskDetails      `json:"disks,omitempty"`
	DroppedDiskRoots      int                     `json:"dropped_disk_roots"`
}

type AdminResourceSource struct {
	Scope     string `json:"scope"`  // host, virtualized_host, cgroup, network_namespace
	Source    string `json:"source"` // procfs, lxcfs, cgroup_v1, cgroup_v2
	Available bool   `json:"available"`
}

// AdminProcessStats describes this Silo process. Linux I/O counters also include
// waited-for children; CPU and RSS describe the process itself. HeapLiveBytes is the last GC's
// marked live Go heap; RSS includes resident native allocations and mappings.
// Their difference is not an exact native allocation measurement.
type AdminProcessStats struct {
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

// AdminCgroupCPUStats pairs usage and limits from the same visible cgroup level.
// Ancestor limits outside the process's cgroup namespace are not observable.
type AdminCgroupCPUStats struct {
	Version          string   `json:"version"`
	Scope            string   `json:"scope"` // leaf or ancestor
	QuotaCores       *float64 `json:"quota_cores,omitempty"`
	UsageSeconds     *float64 `json:"usage_seconds,omitempty"`
	ThrottledSeconds *float64 `json:"throttled_seconds,omitempty"`
	Periods          *uint64  `json:"periods,omitempty"`
	ThrottledPeriods *uint64  `json:"throttled_periods,omitempty"`
	PressureSomePct  *float64 `json:"pressure_some_pct,omitempty"`
}

type AdminCgroupMemoryStats struct {
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

// AdminChildStats is a bounded point-in-time view of owned FFmpeg processes. CPU and
// RSS are sums of live processes; CPU can decrease when a child exits. RSS can
// count shared pages more than once. Short-lived work requires owner lifecycle
// counters and is deliberately not represented as a cumulative total here.
type AdminChildStats struct {
	Workload      string  `json:"workload"`
	Sampled       int     `json:"sampled"`
	Unavailable   int     `json:"unavailable"`
	Truncated     bool    `json:"truncated"`
	ResidentBytes uint64  `json:"resident_bytes"`
	CPUSeconds    float64 `json:"cpu_seconds"`
}

// AdminDiskDetails extends operational snapshots without adding fields to the frozen
// v1 SystemStats wire shape. Role matches System.Disks; values have its freshness.
type AdminDiskDetails struct {
	Role        string  `json:"role"`
	InodesUsed  *uint64 `json:"inodes_used,omitempty"`
	InodesTotal *uint64 `json:"inodes_total,omitempty"`
}

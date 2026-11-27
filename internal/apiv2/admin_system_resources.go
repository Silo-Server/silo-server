package apiv2

// AdminResourceAttribution names the population behind each system reading.
// Separate scopes overlap: never add process RSS, child RSS and cgroup memory.
// Missing optional values were not measured. InstanceID is random per sampler
// lifetime, not a hostname, and identifies a response behind a load balancer.
type AdminResourceAttribution struct {
	InstanceID            string                  `json:"instance_id" doc:"Random identifier for this sampler lifetime; identifies which process answered behind a load balancer without exposing a hostname."`
	SampleIntervalSeconds float64                 `json:"sample_interval_seconds" doc:"Configured sampling cadence in seconds."`
	SampleDurationSeconds float64                 `json:"sample_duration_seconds" doc:"Wall-clock duration of the last completed sampling pass in seconds."`
	CPU                   AdminResourceSource     `json:"cpu" doc:"Scope, source, and availability of system.cpu_pct and system.cores."`
	Memory                AdminResourceSource     `json:"memory" doc:"Scope, source, and availability of system.mem_used_mb and system.mem_total_mb."`
	Load                  AdminResourceSource     `json:"load" doc:"Scope, source, and availability of system.load1."`
	Network               AdminResourceSource     `json:"network" doc:"Scope, source, and availability of system.net_rx_bps and system.net_tx_bps."`
	Process               *AdminProcessStats      `json:"process,omitempty" doc:"Resource usage of this Silo process alone. Overlaps with children and cgroup_memory; do not add them together."`
	CgroupCPU             *AdminCgroupCPUStats    `json:"cgroup_cpu,omitempty" doc:"Visible cgroup CPU usage, quota, throttling, and pressure; omitted when unavailable."`
	CgroupMemory          *AdminCgroupMemoryStats `json:"cgroup_memory,omitempty" doc:"Visible cgroup memory usage, limit, events, and pressure; omitted when unavailable. Overlaps with process and children; do not add them together."`
	Children              *AdminChildStats        `json:"children,omitempty" doc:"Bounded point-in-time snapshot of live owned FFmpeg children; omitted when unavailable. Overlaps with cgroup_memory; do not add to cgroup_memory."`
	Disks                 []AdminDiskDetails      `json:"disks,omitempty" doc:"Inode counts keyed by disk role, carrying the matching system.disks entry's freshness; omitted when none."`
	DroppedDiskRoots      int                     `json:"dropped_disk_roots" doc:"Configured media roots beyond the bounded disk sampling cap that were not probed."`
}

type AdminResourceSource struct {
	Scope     string `json:"scope" doc:"Population measured: host, virtualized_host, cgroup, or network_namespace; new values may appear."`
	Source    string `json:"source" doc:"Operating-system source: procfs, lxcfs, cgroup_v1, or cgroup_v2; new values may appear."`
	Available bool   `json:"available" doc:"True when the corresponding system reading was measured in this sample."`
}

// AdminProcessStats describes this Silo process. Linux I/O counters also include
// waited-for children; CPU and RSS describe the process itself. HeapLiveBytes is the last GC's
// marked live Go heap; RSS includes resident native allocations and mappings.
// Their difference is not an exact native allocation measurement.
type AdminProcessStats struct {
	ResidentBytes *uint64  `json:"resident_bytes,omitempty" doc:"Resident set size (RSS) of this Silo server process alone in bytes; excludes FFmpeg children and separate database/cache processes."`
	VirtualBytes  *uint64  `json:"virtual_bytes,omitempty" doc:"Virtual memory size of this Silo process in bytes."`
	CPUSeconds    *float64 `json:"cpu_seconds,omitempty" doc:"Cumulative CPU time consumed by this Silo process in seconds (user + system)."`
	CPUPct        *float64 `json:"cpu_pct,omitempty" minimum:"0" maximum:"100" doc:"This process's CPU over the last sample window, on the same scale and capacity as system.cpu_pct. Omitted when the window was not measured; a measured idle process is 0."`
	Threads       *uint64  `json:"threads,omitempty" doc:"OS thread count of this Silo process."`
	OpenFDs       *uint64  `json:"open_fds,omitempty" doc:"Open file descriptors held by this Silo process."`
	MaxFDs        *uint64  `json:"max_fds,omitempty" doc:"Soft file-descriptor limit (RLIMIT_NOFILE) of this Silo process."`
	ReadBytes     *uint64  `json:"read_bytes,omitempty" doc:"Cumulative storage bytes read from /proc/PID/io; on Linux this includes I/O attributed to waited-for children."`
	WriteBytes    *uint64  `json:"write_bytes,omitempty" doc:"Cumulative storage bytes written from /proc/PID/io; on Linux this includes I/O attributed to waited-for children."`
	HeapLiveBytes *uint64  `json:"heap_live_bytes,omitempty" doc:"Marked live Go heap after the last GC cycle in bytes (subset of resident_bytes; subtracting from RSS does not yield exact native allocations)."`
	GoMemoryBytes *uint64  `json:"go_memory_bytes,omitempty" doc:"Total memory mapped by the Go runtime in bytes; this is a runtime reservation, not RSS."`
	Goroutines    *uint64  `json:"goroutines,omitempty" doc:"Live goroutine count in this Silo process."`
}

// AdminCgroupCPUStats pairs usage and limits from the same visible cgroup level.
// Ancestor limits outside the process's cgroup namespace are not observable.
type AdminCgroupCPUStats struct {
	Version          string   `json:"version" doc:"Cgroup hierarchy version: v1 or v2; new values may appear."`
	Scope            string   `json:"scope" doc:"Selected visible cgroup level: leaf or ancestor (an ancestor includes sibling workloads); new values may appear."`
	QuotaCores       *float64 `json:"quota_cores,omitempty" doc:"Effective CPU quota in cores at the selected cgroup level (may reflect a tighter cpuset); omitted when unlimited."`
	UsageSeconds     *float64 `json:"usage_seconds,omitempty" doc:"Cumulative CPU time consumed by the selected cgroup in seconds."`
	ThrottledSeconds *float64 `json:"throttled_seconds,omitempty" doc:"Cumulative CPU time spent throttled by cgroup bandwidth limits in seconds."`
	Periods          *uint64  `json:"periods,omitempty" doc:"Total cgroup CPU bandwidth enforcement periods elapsed."`
	ThrottledPeriods *uint64  `json:"throttled_periods,omitempty" doc:"Enforcement periods during which the cgroup was CPU-throttled."`
	PressureSomePct  *float64 `json:"pressure_some_pct,omitempty" doc:"Linux PSI cpu some avg10 percentage (10-second average)."`
}

type AdminCgroupMemoryStats struct {
	Version         string   `json:"version" doc:"Cgroup hierarchy version: v1 or v2; new values may appear."`
	Scope           string   `json:"scope" doc:"Selected visible cgroup level: leaf or ancestor (an ancestor includes sibling workloads); new values may appear."`
	CurrentBytes    *uint64  `json:"current_bytes,omitempty" doc:"Total memory charged to the selected cgroup in bytes, including page cache. In all-in-one containers this includes co-resident services such as bundled Postgres or Redis."`
	LimitBytes      *uint64  `json:"limit_bytes,omitempty" doc:"Concrete memory limit of the selected cgroup in bytes; omitted when unlimited."`
	WorkingSetBytes *uint64  `json:"working_set_bytes,omitempty" doc:"Selected cgroup memory charge minus inactive file pages in bytes; closest single figure for container memory use, though an ancestor scope includes sibling workloads and it is not an exact OOM predictor."`
	SwapBytes       *uint64  `json:"swap_bytes,omitempty" doc:"Swap usage of the selected cgroup in bytes."`
	HighEvents      *uint64  `json:"high_events,omitempty" doc:"Count of memory.high throttle events on cgroup v2."`
	MaxEvents       *uint64  `json:"max_events,omitempty" doc:"Count of memory.max (v2) or failcnt (v1) limit-hit events."`
	OOMEvents       *uint64  `json:"oom_events,omitempty" doc:"Count of cgroup OOM events."`
	OOMKills        *uint64  `json:"oom_kills,omitempty" doc:"Count of processes killed by the cgroup OOM killer."`
	PressureSomePct *float64 `json:"pressure_some_pct,omitempty" doc:"Linux PSI memory some avg10 percentage (10-second average)."`
	PressureFullPct *float64 `json:"pressure_full_pct,omitempty" doc:"Linux PSI memory full avg10 percentage (10-second average)."`
}

// AdminChildStats is a bounded point-in-time view of owned FFmpeg processes. CPU
// seconds and RSS are sums of live processes; CPU seconds can decrease when a
// child exits. RSS can count shared pages more than once. Short-lived work
// requires owner lifecycle counters and is deliberately not represented as a
// cumulative total here.
type AdminChildStats struct {
	Workload      string   `json:"workload" doc:"Owned child workload sampled (for example ffmpeg); new values may appear."`
	Sampled       int      `json:"sampled" doc:"Live owned child processes successfully read in this sample."`
	Unavailable   int      `json:"unavailable" doc:"Live owned child processes whose /proc entries could not be read."`
	Truncated     bool     `json:"truncated" doc:"True when more live owned child processes existed than the per-sample cap (256)."`
	ResidentBytes uint64   `json:"resident_bytes" doc:"Summed resident memory (RSS) in bytes across sampled live owned child processes (transcodes, remuxes, media analysis, trickplay and chapter thumbnails); may count shared pages more than once."`
	CPUSeconds    float64  `json:"cpu_seconds" doc:"Summed lifetime CPU seconds of currently live owned child processes. Decreases when a child exits; do not diff as a monotonic counter."`
	CPUPct        *float64 `json:"cpu_pct,omitempty" minimum:"0" maximum:"100" doc:"CPU of live owned FFmpeg children over the last sample window, on the same scale and capacity as system.cpu_pct. A lower bound: a child's last partial window and a child that starts and exits between two samples are missed. Omitted when the window was not measured, including a truncated pass."`
}

// AdminDiskDetails extends operational snapshots without adding fields to the frozen
// v1 SystemStats wire shape. Role matches System.Disks; values have its freshness.
type AdminDiskDetails struct {
	Role        string  `json:"role" doc:"Mount role matching the corresponding system.disks entry (scratch or library-N)."`
	InodesUsed  *uint64 `json:"inodes_used,omitempty" doc:"Inodes currently allocated on the filesystem."`
	InodesTotal *uint64 `json:"inodes_total,omitempty" doc:"Total inodes on the filesystem."`
}

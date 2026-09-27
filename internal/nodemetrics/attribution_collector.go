package nodemetrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	descResourceAvailable         = prometheus.NewDesc("silo_resource_sample_available", "Whether the sampler has published a supported resource sample.", nil, nil)
	descResourceTimestamp         = prometheus.NewDesc("silo_resource_sample_timestamp_seconds", "Unix timestamp at the start of the last completed resource sample.", nil, nil)
	descResourceStale             = prometheus.NewDesc("silo_resource_sample_stale", "Whether the last completed resource sample is older than three sampling intervals or absent.", nil, nil)
	descResourceDuration          = prometheus.NewDesc("silo_resource_sample_duration_seconds", "Duration of the last completed resource sampling pass.", nil, nil)
	descResourceSource            = prometheus.NewDesc("silo_resource_source_available", "Whether a current reading exists for the named resource and measurement scope.", []string{"resource", "scope", "source"}, nil)
	descResourceDroppedDisks      = prometheus.NewDesc("silo_resource_disk_roots_dropped", "Configured disk roots omitted by the bounded sampler.", nil, nil)
	descDiskAvailable             = prometheus.NewDesc("silo_resource_disk_available", "Whether a mount has ever been measured successfully; consult streamapp_node_disk_stale for freshness.", []string{diskMountLabel}, nil)
	descDiskInodesUsed            = prometheus.NewDesc("silo_resource_disk_inodes_used", "Inodes in use on a sampled mount; consult streamapp_node_disk_stale for freshness.", []string{diskMountLabel}, nil)
	descDiskInodesTotal           = prometheus.NewDesc("silo_resource_disk_inodes_total", "Inodes on a sampled mount; consult streamapp_node_disk_stale for freshness.", []string{diskMountLabel}, nil)
	descProcessRead               = prometheus.NewDesc("silo_process_io_read_bytes_total", "Bytes fetched from storage according to Linux procfs for Silo and its waited-for children.", nil, nil)
	descProcessWrite              = prometheus.NewDesc("silo_process_io_write_bytes_total", "Bytes submitted to storage according to Linux procfs for Silo and its waited-for children.", nil, nil)
	descCgroupCPUUsage            = cgroupDesc("cpu_usage_seconds_total", "CPU time consumed by the sampled cgroup.")
	descCgroupCPUQuota            = cgroupDesc("cpu_quota_cores", "Effective visible CPU capacity of the sampled cgroup, including a tighter cpuset.")
	descCgroupCPUThrottle         = cgroupDesc("cpu_throttled_seconds_total", "Time the sampled cgroup has been CPU throttled.")
	descCgroupCPUPeriods          = cgroupDesc("cpu_periods_total", "CPU bandwidth periods for the sampled cgroup.")
	descCgroupCPUThrottledPeriods = cgroupDesc("cpu_throttled_periods_total", "CPU bandwidth periods with throttling for the sampled cgroup.")
	descCgroupCPUPressure         = cgroupDesc("cpu_pressure_some_percent", "Percent of time some tasks stalled for CPU over ten seconds.")
	descCgroupMemoryCurrent       = cgroupDesc("memory_current_bytes", "Raw memory charged to the sampled cgroup, including page cache and children.")
	descCgroupMemoryLimit         = cgroupDesc("memory_limit_bytes", "Visible concrete memory limit of the sampled cgroup.")
	descCgroupMemoryWorking       = cgroupDesc("memory_working_set_bytes", "Memory charge minus inactive file pages; not an exact OOM predictor.")
	descCgroupMemorySwap          = cgroupDesc("memory_swap_bytes", "Swap charged to the sampled cgroup.")
	descCgroupMemoryHigh          = cgroupDesc("memory_high_events_total", "Memory high boundary events in the sampled cgroup.")
	descCgroupMemoryMax           = cgroupDesc("memory_max_events_total", "Memory max boundary events (v1 failcnt) in the sampled cgroup.")
	descCgroupMemoryOOM           = cgroupDesc("memory_oom_events_total", "OOM events in the sampled cgroup.")
	descCgroupMemoryOOMKill       = cgroupDesc("memory_oom_kills_total", "OOM kills charged to the sampled cgroup.")
	descCgroupMemoryPressureSome  = cgroupDesc("memory_pressure_some_percent", "Percent of time some tasks stalled for memory over ten seconds.")
	descCgroupMemoryPressureFull  = cgroupDesc("memory_pressure_full_percent", "Percent of time all non-idle tasks stalled for memory over ten seconds.")
	descChildSampled              = childDesc("sampled", "Live owned child processes sampled.")
	descChildUnavailable          = childDesc("unavailable", "Owned child processes that exited or could not be read during this sample.")
	descChildTruncated            = childDesc("truncated", "Whether the live child sample exceeded its process count cap.")
	descChildResident             = childDesc("resident_bytes", "Sum of resident memory of sampled live children; shared pages can be counted more than once.")
	descChildCPU                  = childDesc("cpu_seconds", "Sum of lifetime CPU time of sampled live children; decreases when a child exits and is not a counter.")
)

func cgroupDesc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc("silo_cgroup_"+name, help, []string{"version", "scope"}, nil)
}
func childDesc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc("silo_resource_children_"+name, help, []string{"workload"}, nil)
}
func boolMetric(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func collectAttribution(ch chan<- prometheus.Metric, snapshot Snapshot) {
	gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, labels...)
	}
	gauge(descResourceAvailable, boolMetric(snapshot.Available))
	gauge(descResourceStale, boolMetric(snapshot.Stale(time.Now())))
	if !snapshot.SampledAt.IsZero() {
		gauge(descResourceTimestamp, float64(snapshot.SampledAt.UnixNano())/1e9)
	}
	if snapshot.System != nil {
		for _, disk := range snapshot.System.Disks {
			if disk.Role != "" {
				gauge(descDiskAvailable, boolMetric(!disk.Unavailable), disk.Role)
			}
		}
	}
	a := snapshot.Attribution
	if a == nil {
		return
	}
	gauge(descResourceDuration, a.SampleDurationSeconds)
	gauge(descResourceDroppedDisks, float64(a.DroppedDiskRoots))
	for _, v := range []struct {
		name   string
		source ResourceSource
	}{{resourceCPU, a.CPU}, {resourceMemory, a.Memory}, {"load", a.Load}, {"network", a.Network}} {
		gauge(descResourceSource, boolMetric(v.source.Available), v.name, v.source.Scope, v.source.Source)
	}
	gauge(descResourceSource, boolMetric(a.Process != nil && a.Process.CPUSeconds != nil), "process", "process", "procfs")
	gauge(descResourceSource, boolMetric(a.Process != nil && a.Process.ReadBytes != nil), "process_io", "process_and_reaped_children", "procfs")
	gauge(descResourceSource, boolMetric(a.CgroupCPU != nil && a.CgroupCPU.UsageSeconds != nil), "cgroup_cpu", scopeCgroup, scopeCgroup)
	gauge(descResourceSource, boolMetric(a.CgroupMemory != nil && a.CgroupMemory.CurrentBytes != nil), "cgroup_memory", scopeCgroup, scopeCgroup)
	u := func(desc *prometheus.Desc, value *uint64, counter bool, labels ...string) {
		if value == nil {
			return
		}
		kind := prometheus.GaugeValue
		if counter {
			kind = prometheus.CounterValue
		}
		ch <- prometheus.MustNewConstMetric(desc, kind, float64(*value), labels...)
	}
	f := func(desc *prometheus.Desc, value *float64, counter bool, labels ...string) {
		if value == nil {
			return
		}
		kind := prometheus.GaugeValue
		if counter {
			kind = prometheus.CounterValue
		}
		ch <- prometheus.MustNewConstMetric(desc, kind, *value, labels...)
	}
	for _, d := range a.Disks {
		u(descDiskInodesUsed, d.InodesUsed, false, d.Role)
		u(descDiskInodesTotal, d.InodesTotal, false, d.Role)
	}
	if p := a.Process; p != nil {
		u(descProcessRead, p.ReadBytes, true)
		u(descProcessWrite, p.WriteBytes, true)
	}
	if c := a.CgroupCPU; c != nil {
		f(descCgroupCPUUsage, c.UsageSeconds, true, c.Version, c.Scope)
		f(descCgroupCPUQuota, c.QuotaCores, false, c.Version, c.Scope)
		f(descCgroupCPUThrottle, c.ThrottledSeconds, true, c.Version, c.Scope)
		u(descCgroupCPUPeriods, c.Periods, true, c.Version, c.Scope)
		u(descCgroupCPUThrottledPeriods, c.ThrottledPeriods, true, c.Version, c.Scope)
		f(descCgroupCPUPressure, c.PressureSomePct, false, c.Version, c.Scope)
	}
	if c := a.CgroupMemory; c != nil {
		u(descCgroupMemoryCurrent, c.CurrentBytes, false, c.Version, c.Scope)
		u(descCgroupMemoryLimit, c.LimitBytes, false, c.Version, c.Scope)
		u(descCgroupMemoryWorking, c.WorkingSetBytes, false, c.Version, c.Scope)
		u(descCgroupMemorySwap, c.SwapBytes, false, c.Version, c.Scope)
		u(descCgroupMemoryHigh, c.HighEvents, true, c.Version, c.Scope)
		u(descCgroupMemoryMax, c.MaxEvents, true, c.Version, c.Scope)
		u(descCgroupMemoryOOM, c.OOMEvents, true, c.Version, c.Scope)
		u(descCgroupMemoryOOMKill, c.OOMKills, true, c.Version, c.Scope)
		f(descCgroupMemoryPressureSome, c.PressureSomePct, false, c.Version, c.Scope)
		f(descCgroupMemoryPressureFull, c.PressureFullPct, false, c.Version, c.Scope)
	}
	if c := a.Children; c != nil {
		gauge(descChildSampled, float64(c.Sampled), c.Workload)
		gauge(descChildUnavailable, float64(c.Unavailable), c.Workload)
		gauge(descChildTruncated, boolMetric(c.Truncated), c.Workload)
		if c.Unavailable == 0 && !c.Truncated {
			gauge(descChildResident, float64(c.ResidentBytes), c.Workload)
			gauge(descChildCPU, c.CPUSeconds, c.Workload)
		}
	}
}

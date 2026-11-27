package apiv2

import (
	"context"
	"runtime"
	"time"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
)

// AdminResourceSampler reads the immutable sample already published by the host.
// Transport reads never probe devices, mounts, or worker nodes.
type AdminResourceSampler interface{ Snapshot() nodemetrics.Snapshot }

type AdminBuildInfo struct {
	Display     string   `json:"display"`
	Revision    string   `json:"revision"`
	Dirty       bool     `json:"dirty"`
	VCSTime     *Instant `json:"vcs_time,omitempty"`
	BuildNumber uint64   `json:"build_number"`
	BuiltAt     *Instant `json:"built_at,omitempty"`
	Available   bool     `json:"available"`
}
type AdminBuildInfoOutput struct{ Body AdminBuildInfo }
type AdminSystemResources struct {
	Available   bool                      `json:"available" doc:"True when this host can be sampled. False on non-Linux hosts, before the first sample completes, or without a sampler; do not read false as zero load."`
	SampledAt   *Instant                  `json:"sampled_at,omitempty" doc:"When the sample was taken; omitted when no sample exists."`
	System      *AdminSystemStats         `json:"system,omitempty" doc:"Host or cgroup CPU, memory, load, disk, and network sample; omitted when unavailable."`
	GPU         []AdminGPUStats           `json:"gpu" doc:"One entry per visible GPU, always an array. Missing measurements are omitted while a measured zero remains zero."`
	Stale       bool                      `json:"stale" doc:"True when no sample exists or its age exceeds three sampling intervals."`
	Attribution *AdminResourceAttribution `json:"attribution,omitempty" doc:"Scope and source of each system reading, plus process, cgroup, and owned FFmpeg child detail; omitted when none is reported."`
}
type AdminSystemStats struct {
	CPUPct     int              `json:"cpu_pct" doc:"Aggregate busy percentage across available cores over the last sampling interval (0-100; idle and iowait count as not busy). Uses cgroup usage against cgroup quota when a CPU limit binds; attribution.cpu reports which."`
	Load1      float64          `json:"load1" doc:"1-minute load average, including tasks blocked on storage I/O. Always host-wide (or lxcfs-virtualized); read relative to cores."`
	Cores      int              `json:"cores" doc:"CPUs available to this process: cgroup CPU quota rounded up when limited, otherwise all CPUs the kernel reports."`
	MemUsedMB  int64            `json:"mem_used_mb" doc:"Memory in use in MiB. Matches the cgroup working set when a concrete memory limit binds, otherwise the host; paired with mem_total_mb from the same domain (see attribution.memory)."`
	MemTotalMB int64            `json:"mem_total_mb" doc:"Total memory capacity in MiB, from the same host or cgroup domain as mem_used_mb."`
	Disks      []AdminDiskStats `json:"disks" doc:"Sampled mounts (transcode scratch first, then library roots), deduplicated by filesystem and bounded."`
	NetRxBps   int64            `json:"net_rx_bps" doc:"Aggregate received network throughput in bits per second, excluding loopback. Covers Silo's network namespace (container interfaces under bridge Docker, or host NICs), not streaming traffic alone; use GET /api/v2/admin/stats/timeseries for Silo viewer egress."`
	NetTxBps   int64            `json:"net_tx_bps" doc:"Aggregate transmitted network throughput in bits per second, excluding loopback. Covers Silo's network namespace (all traffic, not streaming alone); use GET /api/v2/admin/stats/timeseries for Silo viewer egress."`
}
type AdminDiskStats struct {
	Path        string  `json:"path,omitempty" doc:"Where the mount is; omitted for scoped API keys"`
	Role        string  `json:"role,omitempty" doc:"Mount role: scratch for the transcode working directory, or library-N positionally for each media root."`
	UsedGB      float64 `json:"used_gb" doc:"Used space on the filesystem in GiB (bytes divided by 1024^3)."`
	TotalGB     float64 `json:"total_gb" doc:"Usable filesystem capacity in GiB (bytes used plus bytes available to this process, excluding blocks reserved for root)."`
	Stale       bool    `json:"stale,omitzero" doc:"True when the mount's last probe did not finish in time and this reading is carried forward."`
	Unavailable bool    `json:"unavailable,omitzero" doc:"True when the mount has never been measured successfully; used_gb and total_gb are meaningless when true."`
	Scratch     bool    `json:"scratch,omitzero" doc:"True for the transcode working directory mount (at most one entry per sample)."`
}
type AdminGPUStats struct {
	Device        string `json:"device" doc:"Render device path (for example /dev/dri/renderD128) or CUDA index/UUID when no DRM render node is visible."`
	Vendor        string `json:"vendor,omitempty" doc:"GPU vendor (for example nvidia, intel, amd); new values may appear."`
	Sessions      int    `json:"sessions" doc:"Active hardware-accelerated sessions attributed to this GPU."`
	VideoBusyPct  *int   `json:"video_busy_pct,omitempty" doc:"Video engine busy percentage when reported; omitted when not measurable."`
	RenderBusyPct *int   `json:"render_busy_pct,omitempty" doc:"Render/3D engine busy percentage when reported; omitted when not measurable."`
	TotalBusyPct  *int   `json:"total_busy_pct,omitempty" doc:"Overall GPU busy percentage when reported; omitted when not measurable."`
	VRAMUsedMB    *int64 `json:"vram_used_mb,omitempty" doc:"GPU memory in use in MiB when reported; omitted when not measurable."`
	VRAMTotalMB   *int64 `json:"vram_total_mb,omitempty" doc:"Total GPU memory in MiB when reported; omitted when not measurable."`
	Source        string `json:"source" doc:"Measurement source (fdinfo, nvidia-smi, fdinfo+nvidia-smi, unavailable); new values may appear."`
}
type AdminSystemResourcesOutput struct{ Body AdminSystemResources }

type AdminResourceCapabilities struct {
	Capability
	InstanceAttribution bool `json:"instance_attribution" doc:"Responses include attribution.instance_id and per-metric scope and source metadata."`
	ProcessResources    bool `json:"process_resources" doc:"Responses may include attribution.process and attribution.children."`
	CgroupResources     bool `json:"cgroup_resources" doc:"Responses may include attribution.cgroup_cpu and attribution.cgroup_memory."`
	SampleFreshness     bool `json:"sample_freshness" doc:"Responses include sampled_at, stale, and attribution sample interval and duration."`
	ProcessCPUPercent   bool `json:"process_cpu_percent" doc:"Responses may include attribution.process.cpu_pct and attribution.children.cpu_pct."`
}
type AdminResourceCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminResourceCapabilities
}

func registerAdminSystem(reg *Registry) {
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/system/resources/capabilities", "getAdminResourceCapabilities", "admin-settings", "Discover resource attribution and freshness support. Individual measurements may be unavailable."), Class: ClassActingAdmin}, func(context.Context, *CapabilityInput) (*AdminResourceCapabilitiesOutput, error) {
		state := StateAvailable
		if reg.deps.AdminResourceSampler == nil {
			state = StateNotConfigured
		} else if runtime.GOOS != "linux" {
			state = StateUnsupported
		}
		return &AdminResourceCapabilitiesOutput{Body: AdminResourceCapabilities{Capability: Capability{State: state}, InstanceAttribution: true, ProcessResources: true, CgroupResources: true, SampleFreshness: true, ProcessCPUPercent: true}}, nil
	})
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/system/build", "getAdminBuildInfo", "admin-settings", "Inspect build metadata; use capabilities for feature detection."), Class: ClassActingAdmin}, getAdminBuildInfo)
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/system/resources", "getAdminSystemResources", "admin-settings", "Read this API host's last resource sample without probing hardware."), Class: ClassActingAdmin}, reg.getAdminSystemResources)
}
func buildInstant(value string) (*Instant, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || t.IsZero() {
		return nil, NewProblem(TypeInternalError, "Invalid build timestamp")
	}
	return new(NewInstant(t)), nil
}
func getAdminBuildInfo(context.Context, *struct{}) (*AdminBuildInfoOutput, error) {
	b := buildinfo.Current()
	vcs, err := buildInstant(b.VCSTime)
	if err != nil {
		return nil, err
	}
	built, err := buildInstant(b.BuiltAt)
	if err != nil {
		return nil, err
	}
	return &AdminBuildInfoOutput{Body: AdminBuildInfo{Display: b.Display, Revision: b.Revision, Dirty: b.Dirty, VCSTime: vcs, BuildNumber: b.BuildNumber, BuiltAt: built, Available: b.Available}}, nil
}
func (reg *Registry) getAdminSystemResources(ctx context.Context, _ *struct{}) (*AdminSystemResourcesOutput, error) {
	out := AdminSystemResources{GPU: []AdminGPUStats{}, Stale: true}
	if reg.deps.AdminResourceSampler == nil {
		return &AdminSystemResourcesOutput{Body: out}, nil
	}
	s := reg.deps.AdminResourceSampler.Snapshot()
	if scopedAPIKey(ctx) {
		s = s.RedactPaths()
	}
	out.Available = s.Available
	out.Stale = s.Stale(time.Now())
	out.Attribution = adminResourceAttribution(s.Attribution)
	if !s.SampledAt.IsZero() {
		out.SampledAt = new(NewInstant(s.SampledAt))
	}
	out.System = adminSystemStatsOf(s.System)
	out.GPU = adminGPUStatsOf(s.GPU)
	return &AdminSystemResourcesOutput{Body: out}, nil
}

func adminSystemStatsOf(v *nodemetrics.SystemStats) *AdminSystemStats {
	if v == nil {
		return nil
	}
	out := &AdminSystemStats{CPUPct: v.CPUPct, Load1: v.Load1, Cores: v.Cores, MemUsedMB: v.MemUsedMB, MemTotalMB: v.MemTotalMB, NetRxBps: v.NetRxBps, NetTxBps: v.NetTxBps, Disks: []AdminDiskStats{}}
	for _, d := range v.Disks {
		out.Disks = append(out.Disks, AdminDiskStats(d))
	}
	return out
}

func adminGPUStatsOf(gpus []nodemetrics.GPUStats) []AdminGPUStats {
	out := []AdminGPUStats{}
	for _, g := range gpus {
		out = append(out, AdminGPUStats(g))
	}
	return out
}

func adminResourceAttribution(a *nodemetrics.ResourceAttribution) *AdminResourceAttribution {
	if a == nil {
		return nil
	}
	out := &AdminResourceAttribution{
		InstanceID: a.InstanceID, SampleIntervalSeconds: a.SampleIntervalSeconds, SampleDurationSeconds: a.SampleDurationSeconds,
		CPU: AdminResourceSource(a.CPU), Memory: AdminResourceSource(a.Memory), Load: AdminResourceSource(a.Load), Network: AdminResourceSource(a.Network),
		Process: (*AdminProcessStats)(a.Process), CgroupCPU: (*AdminCgroupCPUStats)(a.CgroupCPU), CgroupMemory: (*AdminCgroupMemoryStats)(a.CgroupMemory), Children: (*AdminChildStats)(a.Children),
		DroppedDiskRoots: a.DroppedDiskRoots,
	}
	for _, d := range a.Disks {
		out.Disks = append(out.Disks, AdminDiskDetails(d))
	}
	return out
}

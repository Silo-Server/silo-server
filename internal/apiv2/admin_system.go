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
	Available   bool                      `json:"available"`
	SampledAt   *Instant                  `json:"sampled_at,omitempty"`
	System      *AdminSystemStats         `json:"system,omitempty"`
	GPU         []AdminGPUStats           `json:"gpu"`
	Stale       bool                      `json:"stale"`
	Attribution *AdminResourceAttribution `json:"attribution,omitempty"`
}
type AdminSystemStats struct {
	CPUPct     int              `json:"cpu_pct"`
	Load1      float64          `json:"load1"`
	Cores      int              `json:"cores"`
	MemUsedMB  int64            `json:"mem_used_mb"`
	MemTotalMB int64            `json:"mem_total_mb"`
	Disks      []AdminDiskStats `json:"disks"`
	NetRxBps   int64            `json:"net_rx_bps"`
	NetTxBps   int64            `json:"net_tx_bps"`
}
type AdminDiskStats struct {
	Path        string  `json:"path,omitempty"`
	Role        string  `json:"role,omitempty"`
	UsedGB      float64 `json:"used_gb"`
	TotalGB     float64 `json:"total_gb"`
	Stale       bool    `json:"stale,omitzero"`
	Unavailable bool    `json:"unavailable,omitzero"`
	Scratch     bool    `json:"scratch,omitzero"`
}
type AdminGPUStats struct {
	Device        string `json:"device"`
	Vendor        string `json:"vendor,omitempty"`
	Sessions      int    `json:"sessions"`
	VideoBusyPct  *int   `json:"video_busy_pct,omitempty"`
	RenderBusyPct *int   `json:"render_busy_pct,omitempty"`
	TotalBusyPct  *int   `json:"total_busy_pct,omitempty"`
	VRAMUsedMB    *int64 `json:"vram_used_mb,omitempty"`
	VRAMTotalMB   *int64 `json:"vram_total_mb,omitempty"`
	Source        string `json:"source"`
}
type AdminSystemResourcesOutput struct{ Body AdminSystemResources }

type AdminResourceCapabilities struct {
	Capability
	InstanceAttribution bool `json:"instance_attribution"`
	ProcessResources    bool `json:"process_resources"`
	CgroupResources     bool `json:"cgroup_resources"`
	SampleFreshness     bool `json:"sample_freshness"`
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
		return &AdminResourceCapabilitiesOutput{Body: AdminResourceCapabilities{Capability: Capability{State: state}, InstanceAttribution: true, ProcessResources: true, CgroupResources: true, SampleFreshness: true}}, nil
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
func (reg *Registry) getAdminSystemResources(context.Context, *struct{}) (*AdminSystemResourcesOutput, error) {
	out := AdminSystemResources{GPU: []AdminGPUStats{}, Stale: true}
	if reg.deps.AdminResourceSampler == nil {
		return &AdminSystemResourcesOutput{Body: out}, nil
	}
	s := reg.deps.AdminResourceSampler.Snapshot()
	out.Available = s.Available
	out.Stale = s.Stale(time.Now())
	out.Attribution = adminResourceAttribution(s.Attribution)
	if !s.SampledAt.IsZero() {
		out.SampledAt = new(NewInstant(s.SampledAt))
	}
	if s.System != nil {
		v := s.System
		out.System = &AdminSystemStats{CPUPct: v.CPUPct, Load1: v.Load1, Cores: v.Cores, MemUsedMB: v.MemUsedMB, MemTotalMB: v.MemTotalMB, NetRxBps: v.NetRxBps, NetTxBps: v.NetTxBps, Disks: []AdminDiskStats{}}
		for _, d := range v.Disks {
			out.System.Disks = append(out.System.Disks, AdminDiskStats(d))
		}
	}
	for _, g := range s.GPU {
		out.GPU = append(out.GPU, AdminGPUStats(g))
	}
	return &AdminSystemResourcesOutput{Body: out}, nil
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

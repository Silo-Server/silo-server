package apiv2

import "context"

// AdminDashboardCapabilities describes support in this API build, not the
// health or configuration of the services behind the individual operations.
type AdminDashboardCapabilities struct {
	Capability
	ServerLayouts    bool `json:"server_layouts"`
	Timeseries       bool `json:"timeseries"`
	PlaybackActivity bool `json:"playback_activity"`
	TopActivity      bool `json:"top_activity"`
	Health           bool `json:"health"`
	LogLevelList     bool `json:"log_level_list"`
	WatchProviders   bool `json:"watch_providers"`
	DownloadsStats   bool `json:"downloads_stats"`
	// EgressTotals reports that GET /admin/stats/timeseries carries the
	// window's egress_bytes and download_egress_bytes totals.
	EgressTotals bool `json:"egress_totals"`
	// EgressBySource reports that GET /admin/stats/timeseries carries
	// egress_sources and each point's egress_by_source.
	EgressBySource bool `json:"egress_by_source"`
	// APIClients reports GET /admin/stats/api-clients and its per-session
	// endpoints read.
	APIClients bool `json:"api_clients"`
}
type AdminDashboardCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminDashboardCapabilities
}

func registerAdminDashboardCapabilities(reg *Registry) {
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/dashboard/capabilities", "getAdminDashboardCapabilities", "admin-observability", "Discover dashboard features supported by v2 in this build. Support does not promise dependency readiness; layout storage is not yet available on v2."), Class: ClassActingAdmin}
	Register(reg, op, func(context.Context, *CapabilityInput) (*AdminDashboardCapabilitiesOutput, error) {
		return &AdminDashboardCapabilitiesOutput{Body: AdminDashboardCapabilities{
			Timeseries: true, PlaybackActivity: true, TopActivity: true, Health: true, WatchProviders: true, DownloadsStats: true, LogLevelList: true, EgressTotals: true, EgressBySource: true, APIClients: true,
		}}, nil
	})
}

func (c AdminDashboardCapabilities) capabilityState() string { return StateAvailable }

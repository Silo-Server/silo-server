package apiv2

import "context"

// AdminDashboardCapabilities describes support in this API build, not the
// health or configuration of the services behind the individual operations.
type AdminDashboardCapabilities struct {
	Capability
	ServerLayouts    bool `json:"server_layouts" doc:"Per-account dashboard layout storage (GET/PUT/DELETE /admin/dashboard/layout) is accepted for clients. Stays false until the layout lifecycle is accepted, although the routes are served."`
	Timeseries       bool `json:"timeseries" doc:"GET /admin/stats/timeseries serves sampled concurrent-stream and viewer-egress history."`
	PlaybackActivity bool `json:"playback_activity" doc:"GET /admin/stats/playback-activity serves rolling playback activity buckets and reliability counts."`
	TopActivity      bool `json:"top_activity" doc:"GET /admin/stats/top-activity serves title and profile watch-history leaderboards."`
	Health           bool `json:"health" doc:"GET /admin/server/status carries the health object."`
	LogLevelList     bool `json:"log_level_list" doc:"GET /admin/logs/app accepts a multi-level filter."`
	WatchProviders   bool `json:"watch_providers" doc:"GET /admin/stats carries the watch_providers array."`
	DownloadsStats   bool `json:"downloads_stats" doc:"GET /admin/stats/downloads serves managed-download aggregates and timeseries points carry download_egress_kbps."`
	EgressTotals     bool `json:"egress_totals" doc:"GET /admin/stats/timeseries carries window-wide egress_bytes and download_egress_bytes totals."`
	EgressBySource   bool `json:"egress_by_source" doc:"GET /admin/stats/timeseries carries egress_sources and each point's egress_by_source split."`
	StreamsByMethod  bool `json:"streams_by_method" doc:"GET /admin/stats/timeseries points carry streams_by_method."`
	APIClients       bool `json:"api_clients" doc:"GET /admin/stats/api-clients and GET /admin/stats/api-clients/{session_id}/endpoints serve per-session API request counts."`
}
type AdminDashboardCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminDashboardCapabilities
}

func registerAdminDashboardCapabilities(reg *Registry) {
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/dashboard/capabilities", "getAdminDashboardCapabilities", "admin-observability", "Discover dashboard features supported by v2 in this build. Support does not promise dependency readiness; server_layouts stays false until the layout lifecycle is accepted, although layout read, save and reset are served."), Class: ClassActingAdmin}
	Register(reg, op, func(context.Context, *CapabilityInput) (*AdminDashboardCapabilitiesOutput, error) {
		return &AdminDashboardCapabilitiesOutput{Body: AdminDashboardCapabilities{
			Timeseries: true, PlaybackActivity: true, TopActivity: true, Health: true, WatchProviders: true, DownloadsStats: true, LogLevelList: true, EgressTotals: true, EgressBySource: true, StreamsByMethod: true, APIClients: true,
		}}, nil
	})
}

func (c AdminDashboardCapabilities) capabilityState() string { return StateAvailable }

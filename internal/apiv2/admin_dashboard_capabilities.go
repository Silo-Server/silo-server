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
			Timeseries: true, PlaybackActivity: true, TopActivity: true, Health: true, WatchProviders: true, DownloadsStats: true, LogLevelList: true,
		}}, nil
	})
}

func (c AdminDashboardCapabilities) capabilityState() string { return StateAvailable }

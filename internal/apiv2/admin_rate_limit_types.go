package apiv2

import "github.com/Silo-Server/silo-server/internal/api/handlers"

type AdminRateLimitUpdate struct {
	Enabled            *bool                                       `json:"enabled,omitempty"`
	Backend            string                                      `json:"backend,omitempty"`
	GlobalReqPerSecond *float64                                    `json:"global_requests_per_second,omitempty"`
	Tiers              map[string]AdminRateLimitTierUpdate         `json:"tiers,omitempty"`
	IPReqPerSecond     *float64                                    `json:"ip_requests_per_second,omitempty"`
	IPReqPerMinute     *float64                                    `json:"ip_requests_per_minute,omitempty"`
	IPBurst            *int                                        `json:"ip_burst,omitempty"`
	AuthEndpoints      map[string]AdminRateLimitAuthEndpointUpdate `json:"auth_endpoints,omitempty"`
}

type AdminRateLimitTierUpdate struct {
	RequestsPerSecond *float64 `json:"requests_per_second,omitempty"`
	RequestsPerMinute *float64 `json:"requests_per_minute,omitempty"`
	Burst             *int     `json:"burst,omitempty"`
}

type AdminRateLimitAuthEndpointUpdate struct {
	RequestsPerMinute *float64 `json:"requests_per_minute,omitempty"`
	Burst             *int     `json:"burst,omitempty"`
}

type AdminRateLimitUpdateResult struct {
	Status          string `json:"status"`
	RestartRequired bool   `json:"restart_required"`
}

func (v AdminRateLimitUpdate) command() handlers.AdminRateLimitUpdate {
	out := handlers.AdminRateLimitUpdate{
		Enabled: v.Enabled, Backend: v.Backend, GlobalReqPerSecond: v.GlobalReqPerSecond,
		IPReqPerSecond: v.IPReqPerSecond, IPReqPerMinute: v.IPReqPerMinute, IPBurst: v.IPBurst,
	}
	if v.Tiers != nil {
		out.Tiers = make(map[string]handlers.AdminRateLimitTierUpdate, len(v.Tiers))
		for key, tier := range v.Tiers {
			out.Tiers[key] = handlers.AdminRateLimitTierUpdate{RequestsPerSecond: tier.RequestsPerSecond, RequestsPerMinute: tier.RequestsPerMinute, Burst: tier.Burst}
		}
	}
	if v.AuthEndpoints != nil {
		out.AuthEndpoints = make(map[string]handlers.AdminRateLimitAuthEndpointUpdate, len(v.AuthEndpoints))
		for key, endpoint := range v.AuthEndpoints {
			out.AuthEndpoints[key] = handlers.AdminRateLimitAuthEndpointUpdate{RequestsPerMinute: endpoint.RequestsPerMinute, Burst: endpoint.Burst}
		}
	}
	return out
}

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
	SessionQuota       *AdminRateLimitSessionQuotaUpdate           `json:"session_quota,omitempty"`
}

type AdminRateLimitSessionQuotaUpdate struct {
	Mode            *string  `json:"mode,omitempty" enum:"off,log,enforce" doc:"off counts nothing; log counts and logs sessions over the quota but serves them; enforce refuses them with 429."`
	RequestsPerHour *float64 `json:"requests_per_hour,omitempty" doc:"Rate the session's bucket refills at."`
	Burst           *int     `json:"burst,omitempty" doc:"Requests the session's bucket holds when full."`
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
	if q := v.SessionQuota; q != nil {
		out.SessionQuota = &handlers.AdminRateLimitSessionQuotaUpdate{Mode: q.Mode, RequestsPerHour: q.RequestsPerHour, Burst: q.Burst}
	}
	if v.AuthEndpoints != nil {
		out.AuthEndpoints = make(map[string]handlers.AdminRateLimitAuthEndpointUpdate, len(v.AuthEndpoints))
		for key, endpoint := range v.AuthEndpoints {
			out.AuthEndpoints[key] = handlers.AdminRateLimitAuthEndpointUpdate{RequestsPerMinute: endpoint.RequestsPerMinute, Burst: endpoint.Burst}
		}
	}
	return out
}

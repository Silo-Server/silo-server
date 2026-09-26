package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminRateLimitReadService interface {
	ReadAdminRateLimitConfig(context.Context) (handlers.AdminRateLimitConfigView, error)
}
type AdminRateLimitTier struct {
	RequestsPerSecond float64 `json:"requests_per_second"`
	RequestsPerMinute float64 `json:"requests_per_minute"`
	Burst             int     `json:"burst"`
}
type AdminRateLimitAuthEndpoint struct {
	RequestsPerMinute float64 `json:"requests_per_minute"`
	Burst             int     `json:"burst"`
}
type AdminRateLimitConfig struct {
	Enabled            bool                                  `json:"enabled"`
	Backend            string                                `json:"backend"`
	GlobalReqPerSecond float64                               `json:"global_requests_per_second"`
	Tiers              map[string]AdminRateLimitTier         `json:"tiers"`
	IPReqPerSecond     float64                               `json:"ip_requests_per_second"`
	IPReqPerMinute     float64                               `json:"ip_requests_per_minute"`
	IPBurst            int                                   `json:"ip_burst"`
	AuthEndpoints      map[string]AdminRateLimitAuthEndpoint `json:"auth_endpoints"`
}
type AdminRateLimitStatus struct {
	Active         bool   `json:"active"`
	ActiveBackend  string `json:"active_backend,omitempty"`
	RedisAvailable bool   `json:"redis_available"`
}
type AdminRateLimitConfigOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminRateLimitConfig
}
type AdminRateLimitReadInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminRateLimitStatusOutput struct{ Body AdminRateLimitStatus }

func adminRateLimitConfigOf(v handlers.AdminRateLimitConfigView) AdminRateLimitConfig {
	out := AdminRateLimitConfig{Enabled: v.Enabled, Backend: v.Backend, GlobalReqPerSecond: v.GlobalReqPerSecond, IPReqPerSecond: v.IPReqPerSecond, IPReqPerMinute: v.IPReqPerMinute, IPBurst: v.IPBurst, Tiers: make(map[string]AdminRateLimitTier, len(v.Tiers)), AuthEndpoints: make(map[string]AdminRateLimitAuthEndpoint, len(v.AuthEndpoints))}
	for name, t := range v.Tiers {
		out.Tiers[name] = AdminRateLimitTier{t.RequestsPerSecond, t.RequestsPerMinute, t.Burst}
	}
	for name, e := range v.AuthEndpoints {
		out.AuthEndpoints[name] = AdminRateLimitAuthEndpoint{e.RequestsPerMinute, e.Burst}
	}
	return out
}
func adminRateLimitTag(ctx context.Context, cfg AdminRateLimitConfig) (EntityTag, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return EntityTag{}, err
	}
	return RenderETag("admin-rate-limits:"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx)+":"+viewerScopeDigest(ctx), fmt.Sprintf("%x", sha256.Sum256(raw)), 1), nil
}
func registerAdminRateLimitReads(reg *Registry) {
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/rate-limits/"+path, id, "admin-settings", summary), Class: ClassActingAdmin, ServiceBacked: true}
	}
	get := op("config", "getAdminRateLimitConfig", "Read desired rate-limit settings independently of process runtime observations.")
	get.Conditional = true
	Register(reg, get, func(ctx context.Context, in *AdminRateLimitReadInput) (*AdminRateLimitConfigOutput, error) {
		if reg.deps.AdminRateLimits == nil {
			return nil, unavailable("rate limits")
		}
		v, err := reg.deps.AdminRateLimits.ReadAdminRateLimitConfig(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		cfg := adminRateLimitConfigOf(v)
		tag, err := adminRateLimitTag(ctx, cfg)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := &AdminRateLimitConfigOutput{ETag: tag.String(), Body: cfg}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	Register(reg, op("status", "getAdminRateLimitStatus", "Read process-local limiter backend and configured Redis availability; no reachability probe."), func(ctx context.Context, _ *struct{}) (*AdminRateLimitStatusOutput, error) {
		if reg.deps.AdminRateLimits == nil {
			return nil, unavailable("rate limits")
		}
		v, err := reg.deps.AdminRateLimits.ReadAdminRateLimitConfig(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &AdminRateLimitStatusOutput{Body: AdminRateLimitStatus{v.Active, v.ActiveBackend, v.RedisAvailable}}, nil
	})
}

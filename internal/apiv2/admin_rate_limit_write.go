package apiv2

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminRateLimitWriteService interface {
	UpdateAdminRateLimitConfig(context.Context, handlers.AdminRateLimitUpdate, func(handlers.AdminRateLimitConfigView) error) (handlers.AdminRateLimitUpdateResult, error)
}
type AdminRateLimitUpdateInput struct {
	RawBody     []byte
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminRateLimitUpdate
}
type AdminRateLimitUpdateOutput struct {
	Body AdminRateLimitUpdateResult
}

func registerAdminRateLimitWrite(reg *Registry) {
	op := Operation{Operation: humaOp("PATCH", Prefix+"/admin/rate-limits/config", "updateAdminRateLimitConfig", "admin-settings", "Merge captured rate-limit settings under the settings transaction lock; reloads are process-local and event publication is best effort."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, GuardedReceipt: true, RetrySafety: RetrySafetyNaturalIdempotent}
	Register(reg, op, func(ctx context.Context, in *AdminRateLimitUpdateInput) (*AdminRateLimitUpdateOutput, error) {
		if reg.deps.AdminRateLimitsWrite == nil {
			return nil, unavailable("rate limits")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		var maps struct {
			Tiers map[string]json.RawMessage `json:"tiers"`
			Auth  map[string]json.RawMessage `json:"auth_endpoints"`
		}
		_ = json.Unmarshal(in.RawBody, &maps)
		for _, group := range []map[string]json.RawMessage{maps.Tiers, maps.Auth} {
			for _, raw := range group {
				if string(raw) == "null" {
					return nil, NewProblem(TypeValidationFailed, "Rate-limit entries cannot be null")
				}
				if p := rejectNonNullableNulls(raw, nil); p != nil {
					return nil, p
				}
			}
		}
		result, err := reg.deps.AdminRateLimitsWrite.UpdateAdminRateLimitConfig(ctx, in.Body.command(), func(current handlers.AdminRateLimitConfigView) error {
			tag, err := adminRateLimitTag(ctx, adminRateLimitConfigOf(current))
			if err != nil {
				return err
			}
			if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
				if p.Status == 412 {
					return StaleVersionProblem(tag)
				}
				return p
			}
			return nil
		})
		if err != nil {
			if p, ok := errors.AsType[*Problem](err); ok {
				return nil, p
			}
			if apiErr, ok := errors.AsType[*handlers.APIError](err); ok && apiErr.Status == 400 {
				return nil, NewProblem(TypeValidationFailed, apiErr.Message)
			}
			return nil, serviceProblem(err)
		}
		return &AdminRateLimitUpdateOutput{Body: AdminRateLimitUpdateResult{Status: result.Status, RestartRequired: result.RestartRequired}}, nil
	})
}

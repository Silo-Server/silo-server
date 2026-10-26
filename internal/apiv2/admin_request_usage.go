package apiv2

import (
	"context"
	"net/http"
	"strconv"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// AdminRequestUsageService resolves an account's effective request policy
// and quota use (*requests.Service).
type AdminRequestUsageService interface {
	EffectivePolicy(context.Context, int) (mediarequests.EffectivePolicy, error)
}

// AdminRequestUserUsage is an account's effective request policy and how
// much of its quota the current window has used.
type AdminRequestUserUsage struct {
	RequestsEnabled bool    `json:"requests_enabled" doc:"Server-wide requests switch"`
	Allowed         bool    `json:"allowed" doc:"Whether this account may request now (not blocked by its switch, group, or an old block)"`
	Unlimited       bool    `json:"unlimited"`
	Used            int     `json:"used" minimum:"0" doc:"Requests in the window; 0 when unlimited (not counted)"`
	MaxRequests     int     `json:"max_requests" minimum:"0" doc:"Quota; meaningless when unlimited"`
	WindowDays      int     `json:"window_days" minimum:"1"`
	WindowStart     Instant `json:"window_start"`
	Remaining       int     `json:"remaining" minimum:"0"`
	AutoApprove     bool    `json:"auto_approve"`
}

type AdminRequestUserUsageOutput struct {
	Body AdminRequestUserUsage
}

func adminRequestUserUsageOf(p mediarequests.EffectivePolicy) AdminRequestUserUsage {
	return AdminRequestUserUsage{
		RequestsEnabled: p.RequestsEnabled, Allowed: !p.Blocked, Unlimited: p.Unlimited, Used: p.Used,
		MaxRequests: p.MaxRequests, WindowDays: p.WindowDays, WindowStart: NewInstant(p.WindowStart),
		Remaining: p.Remaining, AutoApprove: p.AutoApprove,
	}
}

// registerAdminRequestUsage registers the per-account request quota read.
func registerAdminRequestUsage(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/request-users/{user_id}/usage", "getAdminRequestUserUsage", "admin", "Report an account's effective request policy and quota use."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminRequestUserInput) (*AdminRequestUserUsageOutput, error) {
		if reg.deps.AdminRequestUsage == nil {
			return nil, unavailable("request usage")
		}
		id, err := strconv.Atoi(string(in.UserID))
		if err != nil || id <= 0 {
			return nil, NewProblem(TypeValidationFailed, "Invalid account ID.")
		}
		if accounts := reg.deps.AdminAccounts; accounts != nil {
			if _, err := accounts.GetAdminAccount(ctx, id); err != nil {
				return nil, adminAccountError(err)
			}
		}
		policy, err := reg.deps.AdminRequestUsage.EffectivePolicy(ctx, id)
		if err != nil {
			return nil, requestProblem(err)
		}
		return &AdminRequestUserUsageOutput{Body: adminRequestUserUsageOf(policy)}, nil
	})
}

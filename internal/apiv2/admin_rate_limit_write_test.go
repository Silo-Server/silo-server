package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func (f *fakeAdminRateLimitReads) UpdateAdminRateLimitConfig(ctx context.Context, req handlers.AdminRateLimitUpdate, guard func(handlers.AdminRateLimitConfigView) error) (handlers.AdminRateLimitUpdateResult, error) {
	current, err := f.ReadAdminRateLimitConfig(ctx)
	if err != nil {
		return handlers.AdminRateLimitUpdateResult{}, err
	}
	if err := guard(current); err != nil {
		return handlers.AdminRateLimitUpdateResult{}, err
	}
	if req.GlobalReqPerSecond != nil {
		f.rate = *req.GlobalReqPerSecond
	}
	return handlers.AdminRateLimitUpdateResult{Status: "ok"}, nil
}

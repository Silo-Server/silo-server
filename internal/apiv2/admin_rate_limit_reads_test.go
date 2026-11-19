package apiv2

import (
	"context"
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminRateLimitReads struct {
	calls  int
	active bool
	rate   float64
}

func (f *fakeAdminRateLimitReads) ReadAdminRateLimitConfig(context.Context) (handlers.AdminRateLimitConfigView, error) {
	f.calls++
	var v handlers.AdminRateLimitConfigView
	if err := json.Unmarshal([]byte(`{"backend":"memory","tiers":{"standard":{"requests_per_second":20,"requests_per_minute":600,"burst":30}},"auth_endpoints":{"login":{"requests_per_minute":10,"burst":5}}}`), &v); err != nil {
		return v, err
	}
	v.Active = f.active
	v.RedisAvailable = f.active
	v.GlobalReqPerSecond = f.rate
	return v, nil
}

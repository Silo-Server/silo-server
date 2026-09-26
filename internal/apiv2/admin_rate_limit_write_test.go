package apiv2

import (
	"context"
	"testing"

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
func TestAdminRateLimitGuardedWrite(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminRateLimitReads{rate: 100}
	deps.AdminRateLimits = f
	deps.AdminRateLimitsWrite = f
	h := NewHandler(deps)
	path := Prefix + "/admin/rate-limits/config"
	requireProblem(t, do(t, h, "PATCH", path, `{"enabled":false}`, bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized service call")
	}
	requireProblem(t, do(t, h, "PATCH", path, `{"enabled":false}`, with(bearer(adminToken), "Accept-Encoding", "identity;q=0")), TypeNotAcceptable)
	if f.calls != 0 {
		t.Fatal("encoding refusal reached service")
	}
	requireProblem(t, do(t, h, "PATCH", path, `{"enabled":false}`, bearer(adminToken)), TypePreconditionRequired)
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	old := rec.Header().Get("ETag")
	headers := bearer(adminToken)
	headers["If-Match"] = old
	rec = do(t, h, "PATCH", path, `{"global_requests_per_second":200}`, headers)
	if rec.Code != 200 || f.rate != 200 || rec.Header().Get("ETag") != "" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, "PATCH", path, `{"global_requests_per_second":300}`, headers), TypePreconditionFailed)
	current := do(t, h, "GET", path, "", bearer(adminToken)).Header().Get("ETag")
	headers["If-Match"] = "*"
	headers["If-None-Match"] = current
	rec = do(t, h, "PATCH", path, `{"global_requests_per_second":300}`, headers)
	requireProblem(t, rec, TypePreconditionFailed)
	if f.rate != 200 || rec.Header().Get("ETag") != current {
		t.Fatal("excluded state changed", rec.Code, rec.Header())
	}
	for _, body := range []string{`{"enabled":null}`, `{"tiers":{"standard":null}}`, `{"auth_endpoints":{"login":{"burst":null}}}`} {
		requireProblem(t, do(t, h, "PATCH", path, body, headers), TypeValidationFailed)
	}
}

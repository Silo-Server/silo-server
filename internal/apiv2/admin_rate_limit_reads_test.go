package apiv2

import (
	"context"
	"encoding/json"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"strings"
	"testing"
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
func TestAdminRateLimitReads(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminRateLimitReads{rate: 100}
	deps.AdminRateLimits = f
	h := NewHandler(deps)
	path := Prefix + "/admin/rate-limits/config"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized service read")
	}
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	tag := rec.Header().Get("ETag")
	if rec.Code != 200 || tag == "" || strings.Contains(rec.Body.String(), `"active"`) || !strings.Contains(rec.Body.String(), `"standard"`) {
		t.Fatal(rec.Code, rec.Body.String(), tag)
	}
	f.active = true
	headers := bearer(adminToken)
	headers["If-None-Match"] = tag
	rec = do(t, h, "GET", path, "", headers)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatal("runtime changed canonical validator", rec.Code, rec.Body.String())
	}
	f.rate = 200
	rec = do(t, h, "GET", path, "", headers)
	if rec.Code != 200 || rec.Header().Get("ETag") == tag {
		t.Fatal("configuration failed to change validator")
	}
	rec = do(t, h, "GET", Prefix+"/admin/rate-limits/status", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"active":true`) || strings.Contains(rec.Body.String(), `"tiers"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminRateLimits = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

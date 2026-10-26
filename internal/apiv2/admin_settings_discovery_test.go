package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"strings"
	"testing"
)

type fakeAdminSettingRead struct{ calls int }

func (f *fakeAdminSettingRead) ReadAdminSetting(_ context.Context, key string) (handlers.AdminSettingValue, error) {
	f.calls++
	if key == "missing" {
		return handlers.AdminSettingValue{}, &handlers.APIError{Status: 404, Message: "Setting not found"}
	}
	return handlers.AdminSettingValue{Key: key, Value: "configured", RestartRequired: true}, nil
}
func TestAdminSettingsDiscovery(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminSettingRead)
	deps.AdminSettingRead = f
	h := NewHandler(deps)
	for _, path := range []string{"settings/redis.url", "playback-routing/capabilities"} {
		requireProblem(t, do(t, h, "GET", Prefix+"/admin/"+path, "", bearer(memberToken)), TypePermissionDenied)
	}
	if f.calls != 0 {
		t.Fatal("unauthorized setting read")
	}
	rec := do(t, h, "GET", Prefix+"/admin/settings/redis.url", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"key":"redis.url"`) || !strings.Contains(rec.Body.String(), `"restart_required":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/settings/missing", "", bearer(adminToken)), TypeNotFound)
	rec = do(t, h, "GET", Prefix+"/admin/playback-routing/capabilities", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"playback_node_routing_v1"`) || !strings.Contains(rec.Body.String(), `"worker_only"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminSettingRead = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", Prefix+"/admin/settings/redis.url", "", bearer(adminToken)), TypeDependencyUnavailable)
}

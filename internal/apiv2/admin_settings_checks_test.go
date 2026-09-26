package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"net/http"
	"strings"
	"testing"
)

type fakeAdminSettingsCheck struct {
	calls  int
	kind   string
	values map[string]string
	dirty  []string
}

func (f *fakeAdminSettingsCheck) CheckAdminSettingsConnection(_ context.Context, kind string, values map[string]string, dirty []string) (handlers.AdminSettingsCheckResult, error) {
	f.calls++
	f.kind = kind
	f.values = values
	f.dirty = dirty
	return handlers.AdminSettingsCheckResult{Success: true, Message: "Checked"}, nil
}
func TestAdminSettingsCheckSingleInvocation(t *testing.T) {
	f := &fakeAdminSettingsCheck{}
	deps := requestDeps(fixtureRequests())
	deps.AdminSettingsChecks = f
	h := NewHandler(deps)
	path := Prefix + "/admin/settings/check/redis"
	requireProblem(t, do(t, h, http.MethodPost, path, `{"values":{},"dirty_keys":[]}`, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"values":{},"dirty_keys":null}`, actingRequestAdmin), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid request invoked provider")
	}
	read := do(t, h, http.MethodPost, path, `{"values":{"redis.url":""},"dirty_keys":["redis.url"]}`, actingRequestAdmin)
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"success":true`) || f.calls != 1 || f.kind != "redis" || len(f.dirty) != 1 || f.values["redis.url"] != "" {
		t.Fatal(read.Code, read.Body.String(), f)
	}
}

package apiv2

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"net/http"
	"strings"
	"testing"
)

type fakeAdminSettingsInspection struct {
	effective bool
	reads     int
	err       error
}

func (f *fakeAdminSettingsInspection) InspectAdminSettings(_ context.Context, effective bool) (map[string]string, error) {
	f.effective = effective
	f.reads++
	return map[string]string{"server.log_level": "debug"}, f.err
}
func (f *fakeAdminSettingsInspection) InspectAdminSensitiveSettings(context.Context) (handlers.AdminSensitiveSettingsStatus, error) {
	f.reads++
	return handlers.AdminSensitiveSettingsStatus{}, f.err
}
func TestAdminSettingsInspection(t *testing.T) {
	f := &fakeAdminSettingsInspection{}
	deps := requestDeps(fixtureRequests())
	deps.AdminSettingsInspection = f
	h := NewHandler(deps)
	for _, suffix := range []string{"", "/effective", "/sensitive-status", "/restart-keys"} {
		path := Prefix + "/admin/settings" + suffix
		denied := do(t, h, http.MethodGet, path, "", nil)
		if denied.Code != 401 {
			t.Fatal(denied.Code, denied.Body.String())
		}
		read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
		if read.Code != 200 {
			t.Fatal(read.Code, read.Body.String())
		}
		if suffix == "/effective" && !f.effective {
			t.Fatal("effective settings not resolved")
		}
		if suffix == "/sensitive-status" && (!strings.Contains(read.Body.String(), `"configured":[]`) || !strings.Contains(read.Body.String(), `"managed_by_env":[]`)) {
			t.Fatal(read.Body.String())
		}
	}
	if f.reads != 3 {
		t.Fatal(f.reads)
	}
	f.err = errors.New("synthetic secret database detail")
	read := do(t, h, http.MethodGet, Prefix+"/admin/settings/effective", "", actingRequestAdmin)
	if read.Code != 500 || strings.Contains(read.Body.String(), "synthetic secret") {
		t.Fatal(read.Code, read.Body.String())
	}
	f.err = handlers.ErrAdminSettingsUnavailable
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/settings/effective", "", actingRequestAdmin), TypeDependencyUnavailable)
}

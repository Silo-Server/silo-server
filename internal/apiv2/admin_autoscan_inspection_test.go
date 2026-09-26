package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminAutoscanInspection struct {
	settings handlers.AdminAutoscanSettingsView
	status   handlers.AdminAutoscanStatusView
	calls    int
}

func (f *fakeAdminAutoscanInspection) ReadAdminAutoscanSettings(context.Context) (handlers.AdminAutoscanSettingsView, error) {
	f.calls++
	return f.settings, nil
}
func (f *fakeAdminAutoscanInspection) ReadAdminAutoscanStatus(context.Context) (handlers.AdminAutoscanStatusView, error) {
	f.calls++
	return f.status, nil
}
func TestAdminAutoscanInspection(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminAutoscanInspection{settings: handlers.AdminAutoscanSettingsView{Enabled: true, DefaultPollIntervalSeconds: 60, DebounceSeconds: 5}}
	deps.AdminAutoscanInspection = f
	h := NewHandler(deps)
	settings := Prefix + "/admin/autoscan/settings"
	status := Prefix + "/admin/autoscan/status"
	for _, path := range []string{settings, status} {
		requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	}
	if f.calls != 0 {
		t.Fatal("unauthorized service read")
	}
	rec := do(t, h, "GET", settings, "", bearer(adminToken))
	tag := rec.Header().Get("ETag")
	if rec.Code != 200 || tag == "" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	headers := bearer(adminToken)
	headers["If-None-Match"] = tag
	rec = do(t, h, "GET", settings, "", headers)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.settings.DebounceSeconds++
	rec = do(t, h, "GET", settings, "", headers)
	if rec.Code != 200 || rec.Header().Get("ETag") == tag {
		t.Fatal("configuration validator did not change", rec.Code)
	}
	rec = do(t, h, "GET", status, "", bearer(adminToken))
	for _, want := range []string{`"sources":[]`, `"running_polls":[]`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatal(rec.Body.String())
		}
	}
	err := json.Unmarshal([]byte(`{"enabled":true,"sources":[{"id":"source","source_config":{"secret":"excluded"},"webhook_url":"excluded","last_run_at":"2026-09-01T02:00:00.123456789+02:00"}],"running_polls":[{"id":44,"started_at":"2026-09-01T02:00:00.123456789+02:00","elapsed_ms":90}],"active_scans":3,"accepted_scans":2,"running_scans":1}`), &f.status)
	if err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, "GET", status, "", bearer(adminToken))
	for _, want := range []string{`"id":"44"`, `2026-09-01T00:00:00.123Z`, `"active_scans":3`, `"path_rewrites":[]`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatal(want, rec.Code, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "excluded") || strings.Contains(rec.Body.String(), "source_config") {
		t.Fatal("secret configuration in observation", rec.Body.String())
	}
	deps.AdminAutoscanInspection = nil
	for _, path := range []string{settings, status} {
		requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
	}
}

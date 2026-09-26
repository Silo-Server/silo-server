package apiv2

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

type fakeJellyfinSettingsWriter struct {
	fakeAdminSettingsWrite
	calls int
}

func (f *fakeJellyfinSettingsWriter) UpdateAdminJellyfinCompatSettings(ctx context.Context, patch handlers.AdminJellyfinCompatSettingsPatch, guard func(handlers.AdminSettingsSnapshot) error) (jellycompat.WebComponentStatus, error) {
	snapshot, _ := f.InspectAdminSettingsSnapshot(ctx)
	if err := guard(snapshot); err != nil {
		return jellycompat.WebComponentStatus{}, err
	}
	f.calls++
	return jellycompat.WebComponentStatus{ServerName: "Synthetic", APIState: "disabled", WebState: "missing"}, nil
}
func TestAdminJellyfinSettingsGuard(t *testing.T) {
	f := &fakeJellyfinSettingsWriter{fakeAdminSettingsWrite: fakeAdminSettingsWrite{stored: map[string]string{"jellyfin_compat.enabled": "false"}}}
	deps := pilotDeps(nil, nil)
	deps.CursorSecret = []byte("synthetic-jellyfin-settings-secret")
	deps.AdminSettingsInspection = f
	deps.AdminSettingsWrite = f
	deps.AdminJellyfinCompatSettings = f
	h := NewHandler(deps)
	path := Prefix + "/admin/jellyfin-compat/settings"
	requireProblem(t, do(t, h, "PATCH", path, `{"web_enabled":true}`, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "PATCH", path, `{"web_enabled":true}`, bearer(adminToken)), TypePreconditionRequired)
	headers := bearer(adminToken)
	headers["If-Match"] = do(t, h, "GET", Prefix+"/admin/settings/effective", "", headers).Header().Get("ETag")
	requireProblem(t, do(t, h, "PATCH", path, `{"web_enabled":null}`, headers), TypeValidationFailed)
	rec := do(t, h, "PATCH", path, `{"web_enabled":true}`, headers)
	if rec.Code != 200 || f.calls != 1 {
		t.Fatal(rec.Code, rec.Body.String(), f.calls)
	}
	f.stored["jellyfin_compat.enabled"] = "true"
	requireProblem(t, do(t, h, "PATCH", path, `{"web_enabled":false}`, headers), TypePreconditionFailed)
	if f.calls != 1 {
		t.Fatal("stale patch reached writer")
	}
	deps.AdminJellyfinCompatSettings = nil
	requireProblem(t, do(t, NewHandler(deps), "PATCH", path, `{"web_enabled":true}`, headers), TypeDependencyUnavailable)
}

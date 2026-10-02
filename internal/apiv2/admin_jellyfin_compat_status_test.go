package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
	"strings"
	"testing"
)

type fakeAdminJellyfinStatus struct{ calls int }

func (f *fakeAdminJellyfinStatus) ReadAdminJellyfinCompatStatus(context.Context) (jellycompat.WebComponentStatus, error) {
	f.calls++
	return jellycompat.WebComponentStatus{APIState: "enabled", Enabled: true, WebState: jellycompat.WebComponentInstalling, InstalledAt: "2026-09-01T01:02:03.123456+02:00", Operation: &jellycompat.WebComponentOperationStatus{ID: "local-op", Kind: jellycompat.WebComponentOperationInstall, State: jellycompat.WebComponentOperationRunning, StartedAt: "2026-09-01T01:02:03Z", CompletedAt: "invalid", ProgressPercent: 35}}, nil
}
func TestAdminJellyfinCompatStatusRead(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminJellyfinStatus)
	deps.AdminJellyfinCompatStatus = f
	h := NewHandler(deps)
	path := Prefix + "/admin/jellyfin-compat/status"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized status read")
	}
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"installed_at":"2026-08-31T23:02:03.123Z"`, `"started_at":"2026-09-01T01:02:03.000Z"`, `"prerequisites":[]`, `"id":"local-op"`, `"progress_percent":35`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatal("missing", want, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), `"completed_at"`) {
		t.Fatal("invalid completion date", rec.Body.String())
	}
	deps.AdminJellyfinCompatStatus = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

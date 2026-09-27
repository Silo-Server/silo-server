package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

type fakeJellyfinWeb struct {
	installs, removes int
	req               handlers.AdminJellyfinWebInstallRequest
	running           *jellycompat.WebComponentOperationStatus
	err               error
}

func (f *fakeJellyfinWeb) status(kind jellycompat.WebComponentOperationKind) (jellycompat.WebComponentStatus, error) {
	if f.err != nil {
		return jellycompat.WebComponentStatus{}, f.err
	}
	if f.running != nil {
		return jellycompat.WebComponentStatus{Operation: f.running, WebState: jellycompat.WebComponentInstalling}, jellycompat.ErrWebComponentOperationActive
	}
	return jellycompat.WebComponentStatus{APIState: "enabled", WebState: jellycompat.WebComponentInstalling, Operation: &jellycompat.WebComponentOperationStatus{ID: "op-" + string(kind), Kind: kind, State: jellycompat.WebComponentOperationRunning, StartedAt: "2026-09-06T00:00:00Z"}}, nil
}
func (f *fakeJellyfinWeb) StartAdminJellyfinCompatWebInstall(_ context.Context, req handlers.AdminJellyfinWebInstallRequest) (jellycompat.WebComponentStatus, error) {
	f.installs++
	f.req = req
	return f.status(jellycompat.WebComponentOperationInstall)
}
func (f *fakeJellyfinWeb) StartAdminJellyfinCompatWebRemove(context.Context) (jellycompat.WebComponentStatus, error) {
	f.removes++
	return f.status(jellycompat.WebComponentOperationRemove)
}

func TestAdminJellyfinCompatWebCommands(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeJellyfinWeb)
	deps.AdminJellyfinCompatWeb = f
	h := newTestHandler(t, deps)
	install := Prefix + "/admin/jellyfin-compat/web/install"
	remove := Prefix + "/admin/jellyfin-compat/web/remove"
	requireProblem(t, do(t, h, http.MethodPost, install, `{}`, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, remove, "", bearer(memberToken)), TypePermissionDenied)
	if f.installs+f.removes != 0 {
		t.Fatal("unauthorized command reached the service")
	}
	// An empty body pins nothing; a version pins the release. The body is required.
	requireProblem(t, do(t, h, http.MethodPost, install, "", bearer(adminToken)), TypeUnsupportedMediaType)
	rec := do(t, h, http.MethodPost, install, `{}`, bearer(adminToken))
	if rec.Code != http.StatusAccepted || f.req != (handlers.AdminJellyfinWebInstallRequest{}) || !strings.Contains(rec.Body.String(), `"id":"op-install"`) {
		t.Fatal(rec.Code, rec.Body.String(), f.req)
	}
	rec = do(t, h, http.MethodPost, install, `{"version":"10.11.6"}`, bearer(adminToken))
	if rec.Code != http.StatusAccepted || f.req.Version != "10.11.6" {
		t.Fatal(rec.Code, rec.Body.String(), f.req)
	}
	requireProblem(t, do(t, h, http.MethodPost, install, `{"version":`, bearer(adminToken)), TypeMalformedRequest)
	rec = do(t, h, http.MethodPost, remove, "", bearer(adminToken))
	if rec.Code != http.StatusAccepted || f.removes != 1 || !strings.Contains(rec.Body.String(), `"kind":"remove"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// A repeat while the same kind runs coalesces onto the running operation.
	f.running = &jellycompat.WebComponentOperationStatus{ID: "op-live", Kind: jellycompat.WebComponentOperationInstall, State: jellycompat.WebComponentOperationRunning, StartedAt: "2026-09-06T00:00:00Z"}
	rec = do(t, h, http.MethodPost, install, `{}`, bearer(adminToken))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"id":"op-live"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// The other kind is a conflict.
	requireProblem(t, do(t, h, http.MethodPost, remove, "", bearer(adminToken)), TypeConflict)
	f.running = nil
	f.err = jellycompat.ErrWebInstallerUnavailable
	requireProblem(t, do(t, h, http.MethodPost, install, `{}`, bearer(adminToken)), TypeDependencyUnavailable)
	f.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "web_version_unavailable", Message: "no compatible release"}
	requireProblem(t, do(t, h, http.MethodPost, install, `{}`, bearer(adminToken)), TypeDependencyUnavailable)
	f.err = errors.New("jellyfin web version contains unsafe characters")
	p := requireProblem(t, do(t, h, http.MethodPost, install, `{"version":"x;y"}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	f.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to update setting"}
	requireProblem(t, do(t, h, http.MethodPost, remove, "", bearer(adminToken)), TypeInternalError)
	deps.AdminJellyfinCompatWeb = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, install, `{}`, bearer(adminToken)), TypeDependencyUnavailable)
}

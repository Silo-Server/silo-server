package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeServerRestart struct {
	calls int
	req   handlers.AdminServerRestartRequest
	err   error
}

func (f *fakeServerRestart) RequestAdminServerRestart(_ context.Context, req handlers.AdminServerRestartRequest) (handlers.AdminServerRestartResult, error) {
	f.calls++
	f.req = req
	if f.err != nil {
		return handlers.AdminServerRestartResult{}, f.err
	}
	status := "restart_requested"
	if f.calls > 1 {
		status = "already_requested"
	}
	return handlers.AdminServerRestartResult{Status: status, Message: "synthetic", NotifiedSessions: 2}, nil
}

func TestAdminServerRestart(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeServerRestart)
	deps.AdminServerRestart = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/server/restart"
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized restart reached the service")
	}
	rec := do(t, h, http.MethodPost, path, `{}`, bearer(adminToken))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"restart_requested"`) || !strings.Contains(rec.Body.String(), `"notified_sessions":2`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if f.req != (handlers.AdminServerRestartRequest{}) {
		t.Fatalf("empty body forwarded %+v", f.req)
	}
	// A repeat converges on already_requested, still 202.
	rec = do(t, h, http.MethodPost, path, `{"reason":"config","title":"Maintenance","message":"Back shortly"}`, bearer(adminToken))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"already_requested"`) || f.req.Reason != "config" || f.req.Title != "Maintenance" || f.req.Message != "Back shortly" {
		t.Fatal(rec.Code, rec.Body.String(), f.req)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"reason":`, bearer(adminToken)), TypeMalformedRequest)
	requireProblem(t, do(t, h, http.MethodPost, path, "", bearer(adminToken)), TypeUnsupportedMediaType)
	f.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "service_unavailable", Message: "Server restart is unavailable"}
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, bearer(adminToken)), TypeDependencyUnavailable)
	f.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to request server restart"}
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, bearer(adminToken)), TypeInternalError)
	deps.AdminServerRestart = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, `{}`, bearer(adminToken)), TypeDependencyUnavailable)
}

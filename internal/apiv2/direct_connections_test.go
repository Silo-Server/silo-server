package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/directconn"
)

func TestAdminDirectConnectionsCapabilities(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.DirectConnections = fakeDirectConnections{}
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/direct-connections/capabilities"
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	rec := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var got struct {
		Revision string `json:"revision"`
		State    string `json:"state"`
		Allowed  bool   `json:"allowed"`
		Status   bool   `json:"status"`
		Refresh  bool   `json:"refresh"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision == "" || got.State != StateAvailable || !got.Allowed || !got.Status || !got.Refresh {
		t.Fatalf("capabilities = %+v", got)
	}
	deps.DirectConnections = nil
	rec = do(t, newTestHandler(t, deps), http.MethodGet, path, "", actingRequestAdmin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != StateNotConfigured || got.Allowed || !got.Status || !got.Refresh {
		t.Fatalf("unconfigured capabilities = %+v", got)
	}
}

func TestAdminDirectConnectionsStatus(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, Prefix+"/admin/direct-connections/status", "", actingRequestAdmin)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

type failingDirectConnections struct{}

func (failingDirectConnections) Status(context.Context, bool) (directconn.Status, error) {
	return directconn.Status{}, errors.New("settings read: connection refused")
}

// A failed settings read is a temporary dependency failure, not an internal error.
func TestAdminDirectConnectionsStatusSettingsFailure(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.DirectConnections = failingDirectConnections{}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/admin/direct-connections/status", "", actingRequestAdmin)
	requireProblem(t, rec, TypeDependencyUnavailable)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("503 without Retry-After: %v", rec.Header())
	}
}

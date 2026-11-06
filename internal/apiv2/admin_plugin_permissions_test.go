package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// fakePluginPermissions serves one installation whose requirements a test
// can change, and approves through the check as the real service does: in
// what stands for the approval's transaction, after beforeCheck.
type fakePluginPermissions struct {
	scopes      []string
	approved    []string
	approvals   int
	beforeCheck func()
}

func (f *fakePluginPermissions) current() *plugins.PluginPermissions {
	required := plugins.DeriveRequiredPermissions(nil, f.scopes, []string{"admin:monitor:read", "admin:users"})
	grant := &plugins.PermissionGrant{InstallationID: 7, ApprovedEventAccess: []string{}, ApprovedHostAPIScopes: f.approved}
	return &plugins.PluginPermissions{InstallationID: 7, Required: required, Grant: grant, Pending: len(f.approved) != len(required.RequiredScopes)}
}

func (f *fakePluginPermissions) GetAdminPluginPermissions(context.Context, int) (*plugins.PluginPermissions, error) {
	return f.current(), nil
}

func (f *fakePluginPermissions) ApproveAdminPluginPermissions(_ context.Context, _ int, check plugins.ApprovalCheck) (*plugins.PluginPermissions, error) {
	if f.beforeCheck != nil {
		f.beforeCheck()
	}
	if err := check(f.current()); err != nil {
		return nil, err
	}
	f.approved = f.current().Required.RequiredScopes
	f.approvals++
	return f.current(), nil
}

func (f *fakePluginPermissions) AdminPluginPermissionsPending(context.Context) (map[int]bool, error) {
	return map[int]bool{7: f.current().Pending}, nil
}

// TestAdminPluginPermissionsApprovalGuard covers the approval's validator:
// it is required, it must name the permissions the administrator read, and
// an update that lands between that read and the approval's check is 412
// with the current validator and approves nothing.
func TestAdminPluginPermissionsApprovalGuard(t *testing.T) {
	f := &fakePluginPermissions{scopes: []string{"admin:monitor:read"}}
	deps := requestDeps(fixtureRequests())
	deps.AdminPluginPermissions = f
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/installations/7/permissions"

	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	seen := read.Header().Get("ETag")
	if read.Code != http.StatusOK || seen == "" {
		t.Fatal(read.Code, read.Body.String())
	}
	if cached := do(t, h, http.MethodGet, path, "", with(actingRequestAdmin, "If-None-Match", seen)); cached.Code != http.StatusNotModified {
		t.Fatal(cached.Code, cached.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPut, path+"/approval", "", actingRequestAdmin), TypePreconditionRequired)

	// An update asking for more lands after the read.
	f.scopes = []string{"admin:users"}
	stale := do(t, h, http.MethodPut, path+"/approval", "", with(actingRequestAdmin, "If-Match", seen))
	requireProblem(t, stale, TypePreconditionFailed)
	current := do(t, h, http.MethodGet, path, "", actingRequestAdmin).Header().Get("ETag")
	if stale.Header().Get("ETag") != current || current == seen || f.approvals != 0 {
		t.Fatalf("stale approval: ETag %q, current %q, approvals %d", stale.Header().Get("ETag"), current, f.approvals)
	}

	// The update lands after the read but before the approval's check.
	f.beforeCheck = func() { f.scopes = []string{"admin:monitor:read", "admin:users"} }
	requireProblem(t, do(t, h, http.MethodPut, path+"/approval", "", with(actingRequestAdmin, "If-Match", current)), TypePreconditionFailed)
	if f.approvals != 0 {
		t.Fatalf("an approval landed against requirements the administrator never read")
	}
	f.beforeCheck = nil

	current = do(t, h, http.MethodGet, path, "", actingRequestAdmin).Header().Get("ETag")
	approved := do(t, h, http.MethodPut, path+"/approval", "", with(actingRequestAdmin, "If-Match", current))
	if approved.Code != http.StatusOK || f.approvals != 1 {
		t.Fatal(approved.Code, approved.Body.String())
	}
	if after := do(t, h, http.MethodGet, path, "", actingRequestAdmin).Header().Get("ETag"); approved.Header().Get("ETag") != after || after == current {
		t.Fatalf("approval ETag %q, read after %q, before %q", approved.Header().Get("ETag"), after, current)
	}
}

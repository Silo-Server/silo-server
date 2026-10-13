package apiv2

import (
	"context"
	"net/http"
	"testing"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// fakeGroupAdmin adds access-group request limits to the admin request fake.
type fakeGroupAdmin struct {
	*fakeAdminRequests
	limit mediarequests.GroupLimit
}

func (f *fakeGroupAdmin) GetGroupLimit(_ context.Context, _ mediarequests.Viewer, id int64) (*mediarequests.GroupLimit, error) {
	if id != 2 {
		return nil, mediarequests.ErrNotFound
	}
	out := f.limit
	return &out, nil
}

func (f *fakeGroupAdmin) UpsertGroupLimitConditional(_ context.Context, _ mediarequests.Viewer, in mediarequests.GroupLimit, expected int64) (*mediarequests.GroupLimit, error) {
	if expected != -1 && expected != f.limit.Revision {
		return nil, mediarequests.ErrStaleRevision
	}
	f.writes++
	in.Revision = f.limit.Revision + 1
	f.limit = in
	out := in
	return &out, nil
}

func TestAdminRequestGroupLimit(t *testing.T) {
	f := &fakeGroupAdmin{fakeAdminRequests: fixtureAdminRequests(),
		limit: mediarequests.GroupLimit{GroupID: 2, LimitMode: mediarequests.LimitModeInherit, ApprovalMode: mediarequests.ApprovalModeInherit}}
	deps := requestDeps(fixtureRequests())
	deps.AdminRequests = f
	h := NewHandler(deps)
	path := Prefix + "/admin/request-groups/2/limit"
	body := `{"limit_mode":"custom","max_requests":3,"window_days":14,"approval_mode":"manual"}`

	requireProblem(t, do(t, h, http.MethodGet, path, "", requestOwner), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/request-groups/9/limit", "", actingRequestAdmin), TypeNotFound)
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	var limit AdminRequestGroupLimit
	decodeBody(t, read.Body, &limit)
	if read.Code != 200 || limit.LimitMode != "inherit" || read.Header().Get("ETag") == "" {
		t.Fatal(read.Code, read.Body.String())
	}

	requireProblem(t, do(t, h, http.MethodPut, path, body, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path, `{"limit_mode":"blocked","approval_mode":"inherit"}`, with(actingRequestAdmin, "If-Match", read.Header().Get("ETag"))), TypeValidationFailed)
	saved := do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", read.Header().Get("ETag")))
	if saved.Code != 200 || f.limit.LimitMode != mediarequests.LimitModeCustom || *f.limit.MaxRequests != 3 || f.limit.ApprovalMode != mediarequests.ApprovalModeManual {
		t.Fatalf("%d %s limit=%+v", saved.Code, saved.Body.String(), f.limit)
	}
	stale := do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", read.Header().Get("ETag")))
	requireProblem(t, stale, TypePreconditionFailed)
	if f.writes != 1 {
		t.Fatalf("writes = %d, want 1", f.writes)
	}
}

package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type fakeNodeReload struct {
	calls []int
	rows  []handlers.ForceReloadResult
	err   error
}

func (f *fakeNodeReload) ForceReloadAdminNodes(context.Context) ([]handlers.ForceReloadResult, error) {
	f.calls = append(f.calls, 0)
	return f.rows, f.err
}
func (f *fakeNodeReload) ForceReloadAdminNode(_ context.Context, id int) ([]handlers.ForceReloadResult, error) {
	f.calls = append(f.calls, id)
	return f.rows, f.err
}
func TestAdminNodeReload(t *testing.T) {
	for _, suffix := range []string{"force-reload", "17/force-reload"} {
		t.Run(suffix, func(t *testing.T) {
			f := &fakeNodeReload{rows: []handlers.ForceReloadResult{{NodeID: 17, NodeName: "Synthetic", Status: "ok"}, {NodeID: 18, Status: "error", Error: "private-worker"}}}
			deps := pilotDeps(nil, nil)
			deps.AdminNodeReload = f
			h := NewHandler(deps)
			path := Prefix + "/admin/nodes/" + suffix
			requireProblem(t, do(t, h, "POST", path, "", nil), TypeAuthenticationRequired)
			requireProblem(t, do(t, h, "POST", path, "", bearer(memberToken)), TypePermissionDenied)
			if len(f.calls) != 0 {
				t.Fatal("unauthorized dispatch")
			}
			rec := do(t, h, "POST", path, "", bearer(adminToken))
			wantID := 0
			if suffix != "force-reload" {
				wantID = 17
			}
			if rec.Code != 200 || len(f.calls) != 1 || f.calls[0] != wantID || !strings.Contains(rec.Body.String(), `"node_id":"17"`) || !strings.Contains(rec.Body.String(), `"status":"error"`) || strings.Contains(rec.Body.String(), "private-worker") || rec.Header().Get("Location") != "" {
				t.Fatal(rec.Code, rec.Body.String(), f.calls)
			}
			f.rows = nil
			rec = do(t, h, "POST", path, "", bearer(adminToken))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"results":[]`) {
				t.Fatal(rec.Code, rec.Body.String())
			}
			for _, tc := range []struct {
				err    error
				status int
			}{{nodepool.ErrNodeNotFound, 404}, {handlers.ErrAdminNodesUnavailable, 503}, {errors.New("private-store"), 500}} {
				f.err = tc.err
				rec = do(t, h, "POST", path, "", bearer(adminToken))
				if rec.Code != tc.status || strings.Contains(rec.Body.String(), "private-store") {
					t.Fatal(rec.Code, rec.Body.String())
				}
			}
			deps.AdminNodeReload = nil
			requireProblem(t, do(t, NewHandler(deps), "POST", path, "", bearer(adminToken)), TypeDependencyUnavailable)
		})
	}
}

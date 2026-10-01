package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type fakeNodeCommands struct {
	ids    []int
	err    error
	result *handlers.ReprobeNodeResult
}

func (f *fakeNodeCommands) CheckAdminNode(_ context.Context, id int) (handlers.AdminNodeCheckView, error) {
	f.ids = append(f.ids, id)
	return handlers.AdminNodeCheckView{Healthy: false, HealthPersisted: false}, f.err
}
func (f *fakeNodeCommands) ReprobeAdminNode(_ http.ResponseWriter, r *http.Request, id int) (handlers.ReprobeNodeResult, error) {
	f.ids = append(f.ids, id)
	if r.Context() == nil {
		panic("missing request context")
	}
	if f.result != nil {
		return *f.result, f.err
	}
	return handlers.ReprobeNodeResult{NodeID: id, Status: "error", Error: "private-worker-detail"}, f.err
}
func TestAdminNodeCommands(t *testing.T) {
	for _, suffix := range []string{"check", "reprobe"} {
		t.Run(suffix, func(t *testing.T) {
			f := new(fakeNodeCommands)
			deps := pilotDeps(nil, nil)
			deps.AdminNodeCommands = f
			h := NewHandler(deps)
			path := Prefix + "/admin/nodes/17/" + suffix
			requireProblem(t, do(t, h, "POST", path, "", nil), TypeAuthenticationRequired)
			requireProblem(t, do(t, h, "POST", path, "", bearer(memberToken)), TypePermissionDenied)
			if len(f.ids) != 0 {
				t.Fatal("unauthorized command")
			}
			rec := do(t, h, "POST", path, "", bearer(adminToken))
			if rec.Code != 200 || len(f.ids) != 1 || f.ids[0] != 17 || strings.Contains(rec.Body.String(), "private-worker") {
				t.Fatal(rec.Code, rec.Body.String(), f.ids)
			}
			if suffix == "check" && !strings.Contains(rec.Body.String(), `"health_persisted":false`) {
				t.Fatal(rec.Body.String())
			}
			if suffix == "reprobe" && !strings.Contains(rec.Body.String(), `"node_id":"17"`) {
				t.Fatal(rec.Body.String())
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
			for _, id := range []string{"0", "-1", "2147483648", "1x"} {
				before := len(f.ids)
				rec = do(t, h, "POST", Prefix+"/admin/nodes/"+id+"/"+suffix, "", bearer(adminToken))
				if rec.Code != 422 || len(f.ids) != before {
					t.Fatal(rec.Code, rec.Body.String())
				}
			}
			deps.AdminNodeCommands = nil
			requireProblem(t, do(t, NewHandler(deps), "POST", path, "", bearer(adminToken)), TypeDependencyUnavailable)
		})
	}
}

func TestAdminNodeReprobeSuccess(t *testing.T) {
	f := &fakeNodeCommands{result: &handlers.ReprobeNodeResult{NodeID: 17, NodeName: "Synthetic", Status: "ok", Resolved: "qsv", CapabilityHash: "observed", CapabilitiesRefreshed: true}}
	deps := pilotDeps(nil, nil)
	deps.AdminNodeCommands = f
	rec := do(t, NewHandler(deps), "POST", Prefix+"/admin/nodes/17/reprobe", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, field := range []string{`"node_id":"17"`, `"status":"ok"`, `"capability_hash":"observed"`, `"resolved":"qsv"`, `"capabilities_refreshed":true`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Fatal(rec.Body.String())
		}
	}
	if rec.Header().Get("Location") != "" {
		t.Fatal("unexpected durable-job location")
	}
}

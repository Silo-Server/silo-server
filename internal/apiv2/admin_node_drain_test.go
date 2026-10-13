package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type fakeNodeDrain struct {
	calls []bool
	ids   []int
	err   error
}

func (f *fakeNodeDrain) SetAdminNodeDrain(_ context.Context, id int, requested bool) (*nodepool.Node, error) {
	f.ids = append(f.ids, id)
	f.calls = append(f.calls, requested)
	if f.err != nil {
		return nil, f.err
	}
	state := nodepool.DrainStateNone
	if requested {
		state = nodepool.DrainStateDraining
	}
	return &nodepool.Node{ID: id, Type: nodepool.NodeTypeTranscode, DrainRequested: requested, DrainState: state, ActiveJobs: 3}, nil
}

func TestAdminNodeDrainCommands(t *testing.T) {
	f := new(fakeNodeDrain)
	deps := pilotDeps(nil, nil)
	deps.AdminNodeDrain = f
	h := NewHandler(deps)
	deps.AdminNodeDrain = nil
	missing := NewHandler(deps)
	for _, test := range []struct {
		suffix    string
		requested bool
		want      string
	}{
		{"drain", true, `"drain_requested":true,"drain_state":"draining"`},
		{"resume", false, `"drain_requested":false,"drain_state":"none"`},
	} {
		t.Run(test.suffix, func(t *testing.T) {
			*f = fakeNodeDrain{}
			path := Prefix + "/admin/nodes/17/" + test.suffix
			requireProblem(t, do(t, h, "POST", path, "", nil), TypeAuthenticationRequired)
			requireProblem(t, do(t, h, "POST", path, "", bearer(memberToken)), TypePermissionDenied)
			if len(f.ids) != 0 {
				t.Fatal("unauthorized command reached the service")
			}
			rec := do(t, h, "POST", path, "", bearer(adminToken))
			if rec.Code != 200 || len(f.calls) != 1 || f.calls[0] != test.requested || f.ids[0] != 17 {
				t.Fatal(rec.Code, rec.Body.String(), f.calls)
			}
			for _, field := range []string{`"node_id":"17"`, test.want, `"active_jobs":3`} {
				if !strings.Contains(rec.Body.String(), field) {
					t.Fatalf("body %s lacks %s", rec.Body.String(), field)
				}
			}
			for _, tc := range []struct {
				err    error
				status int
			}{{handlers.ErrNodeNotDrainable, 409}, {nodepool.ErrNodeNotFound, 404}, {handlers.ErrAdminNodesUnavailable, 503}, {errors.New("private-store"), 500}} {
				f.err = tc.err
				rec = do(t, h, "POST", path, "", bearer(adminToken))
				if rec.Code != tc.status || strings.Contains(rec.Body.String(), "private-store") {
					t.Fatal(rec.Code, rec.Body.String())
				}
			}
			before := len(f.ids)
			if rec = do(t, h, "POST", Prefix+"/admin/nodes/0/"+test.suffix, "", bearer(adminToken)); rec.Code != 422 || len(f.ids) != before {
				t.Fatal(rec.Code, rec.Body.String())
			}
			requireProblem(t, do(t, missing, "POST", path, "", bearer(adminToken)), TypeDependencyUnavailable)
		})
	}
}

func TestAdminNodeCapabilitiesAdvertiseDrain(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.AdminNodeDrain = new(fakeNodeDrain)
	rec := do(t, NewHandler(deps), "GET", Prefix+"/admin/nodes/capabilities", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"drain":true`) || !strings.Contains(rec.Body.String(), `"state":"available"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminNodeDrain = nil
	rec = do(t, NewHandler(deps), "GET", Prefix+"/admin/nodes/capabilities", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"not_configured"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, NewHandler(deps), "GET", Prefix+"/admin/nodes/capabilities", "", bearer(memberToken)), TypePermissionDenied)
}

// The node row carries the drain fields, with "none" for a row that has no
// reported state yet.
func TestAdminNodeRowCarriesDrainFields(t *testing.T) {
	row := adminNodeOf(&nodepool.Node{ID: 4, Type: nodepool.NodeTypeTranscode, DrainRequested: true})
	if !row.DrainRequested || row.DrainState != "none" {
		t.Fatalf("row = %+v", row)
	}
	row = adminNodeOf(&nodepool.Node{ID: 4, Type: nodepool.NodeTypeTranscode, DrainState: nodepool.DrainStateDrained})
	if row.DrainRequested || row.DrainState != "drained" {
		t.Fatalf("row = %+v", row)
	}
}

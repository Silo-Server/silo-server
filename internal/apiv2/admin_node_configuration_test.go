package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type fakeNodeConfiguration struct {
	node           nodepool.Node
	calls, effects int
	err            error
	update         nodepool.UpdateNodeInput
}

func (f *fakeNodeConfiguration) ReadAdminNodes(context.Context) ([]*nodepool.Node, error) {
	return []*nodepool.Node{&f.node}, nil
}
func (f *fakeNodeConfiguration) CreateAdminNode(_ context.Context, in nodepool.CreateNodeInput) (*nodepool.Node, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	f.effects++
	f.node.Name = in.Name
	return &f.node, nil
}
func (f *fakeNodeConfiguration) UpdateAdminNode(_ context.Context, id int, in nodepool.UpdateNodeInput, guard func(int64) error) (*nodepool.Node, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if id != f.node.ID {
		return nil, nodepool.ErrNodeNotFound
	}
	if err := guard(f.node.AdminRevision); err != nil {
		return nil, err
	}
	f.effects++
	f.update = in
	f.node.AdminRevision++
	return &f.node, nil
}
func (f *fakeNodeConfiguration) DeleteAdminNode(_ context.Context, id int, guard func(int64) error) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if id != f.node.ID {
		return nodepool.ErrNodeNotFound
	}
	if err := guard(f.node.AdminRevision); err != nil {
		return err
	}
	f.effects++
	return nil
}
func TestAdminNodeConfigurationContract(t *testing.T) {
	f := &fakeNodeConfiguration{node: nodepool.Node{ID: 17, AdminRevision: 1, Name: "Synthetic", Type: "proxy", URL: "http://node.invalid", CreatedAt: time.Unix(1, 0)}}
	deps := pilotDeps(nil, nil)
	deps.AdminNodeConfiguration = f
	deps.AdminNodesRead = f
	h := NewHandler(deps)
	read := do(t, h, "GET", Prefix+"/admin/nodes", "", bearer(adminToken))
	var page struct {
		Items []AdminNode `json:"items"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ConfigETag == "" {
		t.Fatal(read.Body.String())
	}
	tag := page.Items[0].ConfigETag
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			path := Prefix + "/admin/nodes/17"
			body := `{"public_url":null,"hw_accel_override":null,"hw_device_override":null}`
			if method == "POST" {
				path = Prefix + "/admin/nodes"
				body = `{"name":"Synthetic","type":"proxy","url":"http://node.invalid"}`
			}
			if method == "DELETE" {
				body = ""
			}
			before := f.calls
			requireProblem(t, do(t, h, method, path, body, nil), TypeAuthenticationRequired)
			requireProblem(t, do(t, h, method, path, body, bearer(memberToken)), TypePermissionDenied)
			if f.calls != before {
				t.Fatal("unauthorized service call")
			}
			if method != "POST" {
				for _, tc := range []struct {
					match, none string
					status      int
				}{
					{"", "", 428}, {"bad", "", 400}, {`"stale"`, "bad", 412}, {"W/" + tag, "", 412}, {tag, "*", 412}, {tag, "bad", 400},
				} {
					headers := bearer(adminToken)
					if tc.match != "" {
						headers["If-Match"] = tc.match
					}
					if tc.none != "" {
						headers["If-None-Match"] = tc.none
					}
					effects := f.effects
					rec := do(t, h, method, path, body, headers)
					if rec.Code != tc.status || f.effects != effects {
						t.Fatalf("%s %s: %d %s", method, tc.match, rec.Code, rec.Body.String())
					}
				}
			}
			headers := bearer(adminToken)
			headers["If-Match"] = tag
			rec := do(t, h, method, path, body, headers)
			want := 200
			if method == "POST" {
				want = 201
			}
			if method == "DELETE" {
				want = 204
			}
			if rec.Code != want || rec.Header().Get("Location") != "" {
				t.Fatalf("success %d %s", rec.Code, rec.Body.String())
			}
			if method != "DELETE" {
				var result AdminNode
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.ID != "17" || result.ConfigETag == "" || result.ConfigETag != rec.Header().Get("ETag") {
					t.Fatal(rec.Body.String())
				}
				if method == "PUT" {
					if result.ConfigETag == tag {
						t.Fatal("write did not acknowledge its revision")
					}
					for _, value := range []*string{f.update.PublicURL, f.update.HWAccelOverride, f.update.HWDeviceOverride} {
						if value == nil || *value != "" {
							t.Fatal("null clear dropped")
						}
					}
				}
				tag = result.ConfigETag
			}
			f.err = errors.New("private-db-detail")
			rec = do(t, h, method, path, body, headers)
			if rec.Code != 500 || strings.Contains(rec.Body.String(), "private-db") {
				t.Fatal(rec.Code, rec.Body.String())
			}
			f.err = nil
		})
	}
	f.err = nodepool.ErrNodeConfigurationConflict
	requireProblem(t, do(t, h, "POST", Prefix+"/admin/nodes", `{"name":"x","type":"proxy","url":"http://node.invalid"}`, bearer(adminToken)), TypeConflict)
}

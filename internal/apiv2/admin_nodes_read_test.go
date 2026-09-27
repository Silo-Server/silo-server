package apiv2

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type adminNodesReadStub struct {
	nodes []*nodepool.Node
	calls int
}

func (s *adminNodesReadStub) ReadAdminNodes(context.Context) ([]*nodepool.Node, error) {
	s.calls++
	return s.nodes, nil
}
func TestAdminNodesReadPagingAndObservations(t *testing.T) {
	at := time.Date(2026, 9, 1, 1, 2, 3, 123456789, time.UTC)
	a := &nodepool.Node{ID: 2, Name: "same", Type: "transcode", URL: "http://worker.example.test", CreatedAt: at, AdvertisedCapabilitiesHash: new(""), Capabilities: json.RawMessage(`{"schema_version":1,"marker":"stored"}`), LastStats: json.RawMessage(`{"system":{"cpu_pct":3}}`), PhysicalGPUKeys: []string{"gpu-test"}}
	b := &nodepool.Node{ID: 1, Name: "same", Type: "transcode", CreatedAt: at}
	s := &adminNodesReadStub{nodes: []*nodepool.Node{a, nil, b}}
	deps := requestDeps(fixtureRequests())
	deps.AdminNodesRead = s
	h := NewHandler(deps)
	path := Prefix + "/admin/nodes"
	r := do(t, h, "GET", path, "", nil)
	if r.Code != 401 || s.calls != 0 {
		t.Fatal(r.Code, s.calls)
	}
	r = do(t, h, "GET", path+"?limit=1", "", actingRequestAdmin)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"id":"1"`) || !strings.Contains(r.Body.String(), `"last_health_check":null`) || strings.Contains(r.Body.String(), "advertised_capabilities_hash") {
		t.Fatal(r.Code, r.Body.String())
	}
	var page struct {
		Page struct {
			NextCursor string `json:"next_cursor"`
		}
	}
	if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil || page.Page.NextCursor == "" {
		t.Fatal(err, page)
	}
	r = do(t, h, "GET", path+"?limit=1&cursor="+url.QueryEscape(page.Page.NextCursor), "", actingRequestAdmin)
	for _, want := range []string{`"id":"2"`, `"advertised_capabilities_hash":""`, `"marker":"stored"`, `"created_at":"2026-09-01T01:02:03.123Z"`, `"physical_gpu_keys":["gpu-test"]`} {
		if !strings.Contains(r.Body.String(), want) {
			t.Fatal(r.Code, r.Body.String(), want)
		}
	}
	if r.Code != 200 || s.nodes[0] != a || len(s.nodes) != 3 {
		t.Fatal("read mutated supplied node list", r.Code, s.nodes)
	}
	calls := s.calls
	r = do(t, h, "GET", path+"?limit=2&cursor="+url.QueryEscape(page.Page.NextCursor), "", actingRequestAdmin)
	if r.Code != 400 || s.calls != calls {
		t.Fatal(r.Code, s.calls)
	}
	r = do(t, h, "GET", path+"?limit=201", "", actingRequestAdmin)
	if r.Code != 422 || s.calls != calls {
		t.Fatal(r.Code, s.calls)
	}
	s.nodes = nil
	r = do(t, h, "GET", path, "", actingRequestAdmin)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"items":[]`) {
		t.Fatal(r.Code, r.Body.String())
	}
}

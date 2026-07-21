package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSystemBuildInfoResponse(t *testing.T) {
	t.Parallel()

	handler := &SystemHandler{
		buildInfo: buildinfo.Info{
			Display:   "b4c5aae1+dirty",
			Revision:  "b4c5aae18aa653725ac697b29a05eac797576008",
			Dirty:     true,
			VCSTime:   "2026-04-05T22:24:40Z",
			Available: true,
		},
	}

	router := chi.NewRouter()
	router.Get("/admin/system/build", handler.HandleBuildInfo)

	req := httptest.NewRequest(http.MethodGet, "/admin/system/build", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var got buildinfo.Info
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	want := handler.buildInfo
	if got != want {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}

func TestSystemBuildInfoUnavailableResponseShape(t *testing.T) {
	t.Parallel()

	handler := &SystemHandler{
		buildInfo: buildinfo.Info{
			Display:   "unavailable",
			Revision:  "",
			Dirty:     false,
			VCSTime:   "",
			Available: false,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/system/build", nil)
	rec := httptest.NewRecorder()
	handler.HandleBuildInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding raw response: %v", err)
	}

	expected := map[string]any{
		"display":   "unavailable",
		"revision":  "",
		"dirty":     false,
		"vcs_time":  "",
		"available": false,
	}

	for key, want := range expected {
		if got, ok := raw[key]; !ok || got != want {
			t.Fatalf("response[%q] = %#v (present=%v), want %#v", key, got, ok, want)
		}
	}
}

func TestHandleHWAccelAggregatesAllHealthyNodes(t *testing.T) {
	t.Parallel()

	nodeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(playback.HWAccelInfo{
			Resolved:      "qsv",
			RenderDevices: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
			RenderDeviceDetails: []playback.RenderDeviceInfo{
				{Path: "/dev/dri/renderD128", Description: "Intel GPU"},
				{Path: "/dev/dri/renderD129", Description: "Intel GPU"},
			},
		})
	}))
	defer nodeA.Close()
	nodeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(playback.HWAccelInfo{
			Resolved:      "vaapi",
			RenderDevices: []string{"/dev/dri/renderD128"},
			RenderDeviceDetails: []playback.RenderDeviceInfo{
				{Path: "/dev/dri/renderD128", Description: "AMD GPU"},
			},
		})
	}))
	defer nodeB.Close()
	nodeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer nodeDown.Close()

	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{ID: 1, Name: "node-a", URL: nodeA.URL, Enabled: true, Healthy: true},
		{ID: 2, Name: "node-b", URL: nodeB.URL, Enabled: true, Healthy: true},
		{ID: 3, Name: "node-down", URL: nodeDown.URL, Enabled: true, Healthy: true},
		{ID: 4, Name: "node-unhealthy", URL: "http://unreachable.invalid", Enabled: true, Healthy: false},
	})
	handler := &SystemHandler{transcodePool: pool, jwtSecret: "secret"}

	req := httptest.NewRequest(http.MethodGet, "/admin/system/hw-accel", nil)
	rec := httptest.NewRecorder()
	handler.HandleHWAccel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got HWAccelInventory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// Flat primary fields keep the historical single-probe shape, sourced
	// from the first healthy node that answered.
	if got.Resolved != "qsv" || got.Source != "transcode_node" || got.NodeURL != nodeA.URL {
		t.Fatalf("primary = resolved %q source %q node %q, want first node's probe", got.Resolved, got.Source, got.NodeURL)
	}

	// Every healthy node appears, in pool order; the unhealthy one does not.
	if len(got.Nodes) != 3 {
		t.Fatalf("nodes len = %d, want 3: %+v", len(got.Nodes), got.Nodes)
	}
	if got.Nodes[0].NodeName != "node-a" || got.Nodes[0].Resolved != "qsv" || len(got.Nodes[0].RenderDeviceDetails) != 2 {
		t.Fatalf("node-a entry = %+v", got.Nodes[0])
	}
	if got.Nodes[1].NodeName != "node-b" || got.Nodes[1].Resolved != "vaapi" || len(got.Nodes[1].RenderDevices) != 1 {
		t.Fatalf("node-b entry = %+v", got.Nodes[1])
	}
	if got.Nodes[2].NodeName != "node-down" || got.Nodes[2].Error == "" {
		t.Fatalf("failed node entry = %+v, want populated error", got.Nodes[2])
	}
}

func TestHandleHWAccelAllNodeProbesFailFallsBackLocal(t *testing.T) {
	t.Parallel()

	nodeDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer nodeDown.Close()

	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{ID: 1, Name: "node-down", URL: nodeDown.URL, Enabled: true, Healthy: true},
	})
	handler := &SystemHandler{transcodePool: pool, jwtSecret: "secret"}

	req := httptest.NewRequest(http.MethodGet, "/admin/system/hw-accel", nil)
	rec := httptest.NewRecorder()
	handler.HandleHWAccel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got HWAccelInventory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Source != "local" {
		t.Fatalf("source = %q, want local fallback when every node probe fails", got.Source)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Error == "" {
		t.Fatalf("nodes = %+v, want the failing node listed with its error", got.Nodes)
	}
}

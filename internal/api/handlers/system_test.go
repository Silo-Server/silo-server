package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSystemBuildInfoResponse(t *testing.T) {
	t.Parallel()

	handler := &SystemHandler{
		buildInfo: buildinfo.Info{
			Display:     "b4c5aae1+dirty",
			Revision:    "b4c5aae18aa653725ac697b29a05eac797576008",
			Dirty:       true,
			VCSTime:     "2026-04-05T22:24:40Z",
			BuildNumber: 411,
			BuiltAt:     "2026-08-19T19:45:00Z",
			Available:   true,
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
		"display":      "unavailable",
		"revision":     "",
		"dirty":        false,
		"vcs_time":     "",
		"build_number": float64(0),
		"built_at":     "",
		"available":    false,
	}

	for key, want := range expected {
		if got, ok := raw[key]; !ok || got != want {
			t.Fatalf("response[%q] = %#v (present=%v), want %#v", key, got, ok, want)
		}
	}
}

// The inventory fetch is bounded per node by that node's own cold probe
// budget. A node whose caches were just invalidated — a widened device
// override is the common case — legitimately walks past the flat floor, and
// cutting it off reported the node as failed on the Playback settings page
// while it was still inside its own advertised budget.
func TestRemoteInventoryTimeoutScalesWithTheNodeBudget(t *testing.T) {
	t.Parallel()

	advertisedMillis := (90 * time.Second).Milliseconds()
	report := json.RawMessage(fmt.Sprintf(`{"probe_request_timeout_ms":%d}`, advertisedMillis))
	node := &nodepool.Node{URL: "http://node:8082", Capabilities: report}

	handler := &SystemHandler{}
	// Derivation-guard: the expected budget is what the shared pricing rule
	// answers, asserted to exceed the flat floor so the test cannot pass
	// vacuously if the fixture stops out-pricing it.
	want := playback.ColdCapabilityRequestTimeout(report, "", "", remoteNodeInventoryProbeTimeout)
	if want <= remoteNodeInventoryProbeTimeout {
		t.Fatalf("fixture no longer out-prices the floor: got %v, floor %v", want, remoteNodeInventoryProbeTimeout)
	}
	if got := handler.remoteInventoryTimeout(node); got != want {
		t.Fatalf("remoteInventoryTimeout() = %v, want the node's cold budget %v", got, want)
	}

	// A node with no report and no override prices from the cluster policy
	// over the floor — never below it.
	if got := handler.remoteInventoryTimeout(&nodepool.Node{URL: "http://bare:8082"}); got < remoteNodeInventoryProbeTimeout {
		t.Fatalf("remoteInventoryTimeout(bare node) = %v, below the %v floor", got, remoteNodeInventoryProbeTimeout)
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

func TestHandleHWAccelProbeLogRedactsNodeURLSecrets(t *testing.T) {
	const (
		username       = "probe-operator"
		password       = "node-password"
		querySecret    = "query-secret"
		fragmentSecret = "fragment-secret"
	)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{Resolved: "qsv"})
	}))
	defer healthy.Close()
	failed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	failed.Close()
	failedNodeURL := strings.Replace(failed.URL, "http://", "http://"+username+":"+password+"@", 1) +
		"?access_token=" + querySecret + "#" + fragmentSecret

	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{ID: 1, Name: "healthy-node", URL: healthy.URL, Enabled: true, Healthy: true},
		{ID: 2, Name: "failed-node", URL: failedNodeURL, Enabled: true, Healthy: true},
	})
	handler := &SystemHandler{transcodePool: pool, jwtSecret: "jwt-secret"}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	recorder := httptest.NewRecorder()
	handler.HandleHWAccel(recorder, httptest.NewRequest(http.MethodGet, "/admin/system/hw-accel", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	diagnostics := logs.String()
	for _, secret := range []string{username, password, querySecret, fragmentSecret} {
		if strings.Contains(diagnostics, secret) {
			t.Fatalf("capability probe log contains %q: %q", secret, diagnostics)
		}
	}
	if !strings.Contains(diagnostics, failed.URL) {
		t.Fatalf("capability probe log lost sanitized node origin: %q", diagnostics)
	}
}

// The local inventory runs a full hardware walk on the request goroutine, and
// that walk grows with the configured device set — eight Intel render devices
// draw five ffmpeg commands each, already past the API listener's 120-second
// write timeout. Without lifting the deadline the settings page loses its
// response while every probe is still inside its own bound.
func TestHWAccelExtendsTheWriteDeadlineForItsWalk(t *testing.T) {
	devices := make([]string, 0, 8)
	for i := range 8 {
		devices = append(devices, "/dev/dri/renderD"+strconv.Itoa(128+i))
	}
	hwDevice := strings.Join(devices, ",")
	handler := &SystemHandler{
		playback: func() (string, string, string) { return "/nonexistent/ffmpeg", "auto", hwDevice },
	}

	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	handler.HandleHWAccel(recorder, httptest.NewRequest(http.MethodGet, "/admin/system/hw-accel", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	// Sized from the walk's own budget, so it grows with the device set exactly
	// as the walk does. How large that is depends on how the devices classify,
	// which depends on the sysfs of the host running this — an Intel host draws
	// five commands per device and passes 120 seconds at eight of them, while a
	// machine with no such devices prices the same list far lower. So this
	// asserts the derivation rather than a number no host agrees on.
	want := playback.HWAccelWalkTimeout(hwDevice) + hwAccelWriteSlack
	if reserved := recorder.deadline.Sub(before); reserved < want {
		t.Fatalf("reserved %s, want at least the walk's own budget plus slack (%s)", reserved, want)
	}
	// And it is derived from the configured set, not a constant: a single device
	// has to price lower than eight.
	if single := playback.HWAccelWalkTimeout(devices[0]); single >= playback.HWAccelWalkTimeout(hwDevice) {
		t.Fatalf("one device budgets %s and eight budget %s; the walk budget does not follow the device set",
			single, playback.HWAccelWalkTimeout(hwDevice))
	}
}

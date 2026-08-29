package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodemetrics"
)

// newFakeSampler answers with a fixed reading so the handlers under test are
// exercised without a Linux host beneath them.
func newFakeSampler() *nodemetrics.Sampler {
	video, render := 63, 12
	return nodemetrics.NewFixedSamplerForTest(nodemetrics.Snapshot{
		Available: true,
		SampledAt: time.Now(),
		System: &nodemetrics.SystemStats{
			CPUPct: 41, Load1: 3.2, Cores: 16,
			MemUsedMB: 9011, MemTotalMB: 32768,
			Disks:    []nodemetrics.DiskStats{{Path: "/transcode", Role: nodemetrics.ScratchDiskRole, Scratch: true, UsedGB: 210, TotalGB: 500}},
			NetRxBps: 1200000, NetTxBps: 98000000,
		},
		GPU: []nodemetrics.GPUStats{{
			Device: "/dev/dri/renderD128", Vendor: "intel", Sessions: 2,
			VideoBusyPct: &video, RenderBusyPct: &render, Source: nodemetrics.SourceFdinfo,
		}},
	})
}

// Health is what the cluster routes on. Reading the sampler's published
// snapshot — rather than measuring on the request — is what keeps a wedged
// mount or a hung GPU query from turning into a health timeout, so this asserts
// the handler answers promptly and completely.
func TestHealthIncludesResourceSampleWithoutBlocking(t *testing.T) {
	server := newTestServer(t)
	server.metrics = newFakeSampler()

	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.handleHealth(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		answered <- recorder
	}()

	var recorder *httptest.ResponseRecorder
	select {
	case recorder = <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("health handler blocked")
	}

	var body struct {
		Status string `json:"status"`
		System *struct {
			CPUPct int `json:"cpu_pct"`
		} `json:"system"`
		GPU []struct {
			Device   string `json:"device"`
			Sessions int    `json:"sessions"`
		} `json:"gpu"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body: %v (%s)", err, recorder.Body)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q", body.Status)
	}
	if body.System == nil || body.System.CPUPct != 41 {
		t.Fatalf("system = %+v, want the sampled cpu percentage", body.System)
	}
	if len(body.GPU) != 1 || body.GPU[0].Device != "/dev/dri/renderD128" || body.GPU[0].Sessions != 2 {
		t.Fatalf("gpu = %+v", body.GPU)
	}
}

// A node with no sampler — a non-Linux host, or one built before sampling —
// must serve exactly the health response it always did.
func TestHealthOmitsResourceFieldsWithoutASampler(t *testing.T) {
	server := newTestServer(t)

	recorder := httptest.NewRecorder()
	server.handleHealth(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body: %v (%s)", err, recorder.Body)
	}
	for _, key := range []string{"system", "gpu"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s emitted without a sampler: %s", key, recorder.Body)
		}
	}
	if body["status"] != "ok" {
		t.Fatalf("status = %v, want the unchanged response", body["status"])
	}
}

func TestStatusIncludesResourceSample(t *testing.T) {
	server := newTestServer(t)
	server.metrics = newFakeSampler()

	recorder := httptest.NewRecorder()
	server.handleStatus(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))

	var body struct {
		System *json.RawMessage `json:"system"`
		GPU    *json.RawMessage `json:"gpu"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("status body: %v (%s)", err, recorder.Body)
	}
	if body.System == nil || body.GPU == nil {
		t.Fatalf("status omitted the resource sample: %s", recorder.Body)
	}
}

// Operators who scrape must get the same numbers the UI shows, without a
// credential — the same posture the API listener's own /metrics has.
func TestMetricsEndpointIsMountedAndUnauthenticated(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", recorder.Code)
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("GET /metrics returned an empty body")
	}
}

// /api/v1/health takes no bearer token, so anyone who can reach the node can
// read it. A disk entry's path is deployment layout — the transcode scratch
// volume, and on an API host every library root — which is exactly what the
// admin-authenticated resources endpoint exists to gate and what /metrics
// already withholds by labeling series with a role. The fill and the role must
// still be there, or the API's health sweep loses the scratch reading that
// admission control depends on.
func TestHealthOmitsFilesystemPaths(t *testing.T) {
	server := newTestServer(t)
	server.metrics = newFakeSampler()

	recorder := httptest.NewRecorder()
	server.handleHealth(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if body := recorder.Body.String(); strings.Contains(body, "/transcode") {
		t.Fatalf("health body discloses a filesystem path: %s", body)
	}
	var body struct {
		System *struct {
			Disks []struct {
				Path    string  `json:"path"`
				Role    string  `json:"role"`
				Scratch bool    `json:"scratch"`
				UsedGB  float64 `json:"used_gb"`
			} `json:"disks"`
		} `json:"system"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body: %v (%s)", err, recorder.Body)
	}
	if body.System == nil || len(body.System.Disks) != 1 {
		t.Fatalf("system = %+v, want one disk entry", body.System)
	}
	disk := body.System.Disks[0]
	if disk.Path != "" {
		t.Fatalf("disk path = %q, want it withheld", disk.Path)
	}
	if disk.Role != nodemetrics.ScratchDiskRole || !disk.Scratch || disk.UsedGB != 210 {
		t.Fatalf("disk = %+v, want the scratch role and its fill kept", disk)
	}
}

// Redacting for /health must not reach the sampler's own snapshot: /status is
// bearer-authed and the admin resources endpoint is admin-authed, and both are
// meant to show operators where a mount actually is.
func TestStatusKeepsFilesystemPaths(t *testing.T) {
	server := newTestServer(t)
	server.metrics = newFakeSampler()

	recorder := httptest.NewRecorder()
	server.handleHealth(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if got := server.metrics.Snapshot().System.Disks[0].Path; got != "/transcode" {
		t.Fatalf("sampler snapshot path = %q after a health response, want it intact", got)
	}
}

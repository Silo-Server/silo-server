package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
)

func newMetricsProxyServer(t *testing.T) *Server {
	t.Helper()
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "proxy-metrics-secret"
	cfg.Playback.TranscodeDir = t.TempDir()
	w.SetConfigForTest(cfg)
	return NewServer(w, nil)
}

// newFakeSampler answers with a fixed reading so the handlers under test are
// exercised without a Linux host beneath them.
func newFakeSampler() *nodemetrics.Sampler {
	video := 8
	return nodemetrics.NewFixedSamplerForTest(nodemetrics.Snapshot{
		Available: true,
		SampledAt: time.Now(),
		System: &nodemetrics.SystemStats{
			CPUPct: 41, Load1: 3.2, Cores: 16,
			MemUsedMB: 9011, MemTotalMB: 32768,
			Disks:    []nodemetrics.DiskStats{{Path: "/transcode", UsedGB: 210, TotalGB: 500}},
			NetRxBps: 1200000, NetTxBps: 98000000,
		},
		GPU: []nodemetrics.GPUStats{{
			Device: "/dev/dri/renderD128", Vendor: "intel", Sessions: 1,
			VideoBusyPct: &video, Source: nodemetrics.SourceFdinfo,
		}},
	})
}

// A proxy runs ffmpeg too (remux, Dolby Vision RPU strip), so it reports the
// same resource fields a transcode node does — and, like a transcode node, it
// reads them from a published snapshot rather than measuring on the request.
func TestProxyHealthIncludesResourceSampleWithoutBlocking(t *testing.T) {
	server := newMetricsProxyServer(t)
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
			Device string `json:"device"`
		} `json:"gpu"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body: %v (%s)", err, recorder.Body)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q", body.Status)
	}
	if body.System == nil || body.System.CPUPct != 41 {
		t.Fatalf("system = %+v", body.System)
	}
	if len(body.GPU) != 1 || body.GPU[0].Device != "/dev/dri/renderD128" {
		t.Fatalf("gpu = %+v", body.GPU)
	}
}

// Without a sampler the response is byte-for-byte what it always was.
func TestProxyHealthOmitsResourceFieldsWithoutASampler(t *testing.T) {
	server := newMetricsProxyServer(t)

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
}

func TestProxyMetricsEndpointIsMountedAndUnauthenticated(t *testing.T) {
	server := newMetricsProxyServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", recorder.Code)
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("GET /metrics returned an empty body")
	}
}

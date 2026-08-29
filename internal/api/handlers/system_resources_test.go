package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodemetrics"
)

func TestSystemResourcesReportsLocalSample(t *testing.T) {
	t.Parallel()

	sampledAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	total := 71
	video, render := 63, 12
	handler := &SystemHandler{}
	handler.SetResourceSampler(nodemetrics.NewFixedSamplerForTest(nodemetrics.Snapshot{
		Available: true,
		SampledAt: sampledAt,
		System: &nodemetrics.SystemStats{
			CPUPct: 41, Load1: 3.2, Cores: 16,
			MemUsedMB: 9011, MemTotalMB: 32768,
			Disks:    []nodemetrics.DiskStats{{Path: "/transcode", UsedGB: 210, TotalGB: 500}},
			NetRxBps: 1200000, NetTxBps: 98000000,
		},
		GPU: []nodemetrics.GPUStats{{
			Device: "/dev/dri/renderD128", Vendor: "intel", Sessions: 2,
			VideoBusyPct: &video, RenderBusyPct: &render, TotalBusyPct: &total,
			Source: nodemetrics.SourceFdinfo,
		}},
	}))

	rec := httptest.NewRecorder()
	handler.HandleSystemResources(rec, httptest.NewRequest(http.MethodGet, "/admin/system/resources", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Available bool   `json:"available"`
		SampledAt string `json:"sampled_at"`
		System    *struct {
			CPUPct int `json:"cpu_pct"`
			Disks  []struct {
				Path string `json:"path"`
			} `json:"disks"`
		} `json:"system"`
		GPU []struct {
			Device       string `json:"device"`
			TotalBusyPct *int   `json:"total_busy_pct"`
		} `json:"gpu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if !body.Available {
		t.Fatalf("available = false: %s", rec.Body)
	}
	if body.SampledAt != "2026-08-26T12:00:00Z" {
		t.Fatalf("sampled_at = %q", body.SampledAt)
	}
	if body.System == nil || body.System.CPUPct != 41 || len(body.System.Disks) != 1 {
		t.Fatalf("system = %+v", body.System)
	}
	if len(body.GPU) != 1 || body.GPU[0].Device != "/dev/dri/renderD128" {
		t.Fatalf("gpu = %+v", body.GPU)
	}
	if body.GPU[0].TotalBusyPct == nil || *body.GPU[0].TotalBusyPct != 71 {
		t.Fatalf("total_busy_pct = %v, want 71", body.GPU[0].TotalBusyPct)
	}
}

// "Nothing is measuring this host" is a valid answer to "what does this host
// look like", so an unsampled host answers 200 with available:false rather than
// failing the admin page that reads it.
func TestSystemResourcesReportsUnavailableWithoutASampler(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	(&SystemHandler{}).HandleSystemResources(rec, httptest.NewRequest(http.MethodGet, "/admin/system/resources", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if available, _ := body["available"].(bool); available {
		t.Fatalf("available = true without a sampler: %s", rec.Body)
	}
	for _, key := range []string{"system", "gpu", "sampled_at"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s emitted without a sample: %s", key, rec.Body)
		}
	}
}

// A host that cannot be sampled (non-Linux) is reported the same way as one
// with no sampler at all, so a client has one case to handle.
func TestSystemResourcesReportsUnavailableHost(t *testing.T) {
	t.Parallel()

	handler := &SystemHandler{}
	handler.SetResourceSampler(nodemetrics.NewFixedSamplerForTest(nodemetrics.Snapshot{}))

	rec := httptest.NewRecorder()
	handler.HandleSystemResources(rec, httptest.NewRequest(http.MethodGet, "/admin/system/resources", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if available, _ := body["available"].(bool); available {
		t.Fatalf("available = true on an unsampled host: %s", rec.Body)
	}
}

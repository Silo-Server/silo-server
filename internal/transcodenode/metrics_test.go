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

	if got := server.metrics.Snapshot().System.Disks[0].Path; got != "/transcode" {
		t.Fatalf("sampler snapshot path = %q after a health response, want it intact", got)
	}

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

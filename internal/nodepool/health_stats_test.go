package nodepool

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newStatsHealthNode starts a stand-in node whose /health serves a verbatim
// body, so a test can express exactly what an old or new node puts on the wire.
func newStatsHealthNode(t *testing.T, body string) string {
	t.Helper()
	return newStatsHealthNodeFunc(t, func() string { return body })
}

// A node is a worker that may run on hardware this process does not control,
// and last_stats is the one thing it dictates that gets written to the control
// plane database every 30 seconds. An oversized sample is dropped, not stored —
// but whether the node is alive still routes streams, so the verdict survives.
func TestCheckNodeDropsAnOversizedResourceSample(t *testing.T) {
	padding := strings.Repeat("x", maxLastStatsBytes)
	url := newStatsHealthNode(t, `{"status":"ok","active_jobs":3,"egress_kbps":9,
		"system":{"cpu_pct":41,"junk":"`+padding+`"}}`)

	healthy, activeJobs, egressKbps, _, lastStats, _ := CheckNode(context.Background(), &Node{URL: url})
	if !healthy || activeJobs != 3 || egressKbps != 9 {
		t.Fatalf("check = %v/%d/%d, want the health verdict kept", healthy, activeJobs, egressKbps)
	}
	if lastStats != nil {
		t.Fatalf("lastStats = %d bytes, want the oversized sample dropped", len(lastStats))
	}
}

// Past the body cap nothing in the response can be trusted to be well formed,
// so the node reads as not answering rather than as partially believed.
func TestCheckNodeRejectsAnOversizedHealthBody(t *testing.T) {
	url := newStatsHealthNode(t, `{"status":"ok","active_jobs":3,"junk":"`+
		strings.Repeat("x", maxHealthResponseBytes)+`"}`)

	healthy, activeJobs, _, _, lastStats, _ := CheckNode(context.Background(), &Node{URL: url})
	if healthy || activeJobs != 0 || lastStats != nil {
		t.Fatalf("check = %v/%d/%s, want an unreadable body treated as no answer", healthy, activeJobs, lastStats)
	}
}

// Capability reports carry the GPU identities the planner places work on, and
// their fetch is bounded at two minutes — an even wider window for the row to
// be repointed underneath them.
func TestApplyCapabilitiesIgnoresAReportForAReplacedWorker(t *testing.T) {
	pool := newKeyedTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, URL: "http://replacement", Enabled: true}})

	pool.ApplyCapabilities(1, "http://original",
		[]byte(`{"resolved":"qsv","render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],"boot_id":"boot-1"}`),
		"sha256:stale", time.Now(), nil, nil)

	stored := pool.Nodes()[0]
	if len(stored.Capabilities) != 0 || stored.CapabilitiesHash != nil || len(stored.PhysicalGPUKeys) != 0 {
		t.Fatalf("pool took the old worker's capability report: %+v", stored)
	}
}

func TestApplyHealthIgnoresAResultForAReplacedWorker(t *testing.T) {
	pool := newKeyedTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, URL: "http://replacement", Enabled: true}})

	pool.ApplyHealth(1, "http://original", true, 7, 0, "", []byte(`{"system":{"cpu_pct":41}}`), nil, time.Now())

	stored := pool.Nodes()[0]
	if stored.ActiveJobs != 0 || len(stored.LastStats) != 0 || stored.LastHealthCheck != nil {
		t.Fatalf("pool took the old worker's result: %+v", stored)
	}
}

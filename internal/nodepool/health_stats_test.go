package nodepool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newStatsHealthNode starts a stand-in node whose /health serves a verbatim
// body, so a test can express exactly what an old or new node puts on the wire.
func newStatsHealthNode(t *testing.T, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// Node resource stats pass through this package untouched. Parsing them here
// would couple the health sweep to the sampler's schema for no benefit —
// nothing in nodepool routes on them.
func TestCheckNodePassesResourceStatsThroughOpaquely(t *testing.T) {
	url := newStatsHealthNode(t, `{
		"status":"ok",
		"active_jobs":2,
		"egress_kbps":17,
		"capabilities_hash":"sha256:abc",
		"system":{"cpu_pct":41,"load1":3.2,"cores":16,"mem_used_mb":9011,"mem_total_mb":32768,
			"disks":[{"path":"/transcode","used_gb":210,"total_gb":500}],
			"net_rx_bps":1200000,"net_tx_bps":98000000},
		"gpu":[{"device":"/dev/dri/renderD128","vendor":"intel","sessions":2,
			"video_busy_pct":63,"render_busy_pct":12,"source":"fdinfo",
			"future_field_we_do_not_know":true}]
	}`)

	healthy, activeJobs, egressKbps, hash, lastStats := CheckNode(context.Background(), &Node{URL: url})
	if !healthy || activeJobs != 2 || egressKbps != 17 || hash != "sha256:abc" {
		t.Fatalf("check = %v/%d/%d/%q, want the existing fields unchanged", healthy, activeJobs, egressKbps, hash)
	}
	if len(lastStats) == 0 {
		t.Fatal("lastStats is empty; the node reported a sample")
	}

	var decoded struct {
		System struct {
			CPUPct int `json:"cpu_pct"`
		} `json:"system"`
		GPU []map[string]any `json:"gpu"`
	}
	if err := json.Unmarshal(lastStats, &decoded); err != nil {
		t.Fatalf("lastStats is not valid JSON: %v (%s)", err, lastStats)
	}
	if decoded.System.CPUPct != 41 {
		t.Fatalf("system.cpu_pct = %d, want 41", decoded.System.CPUPct)
	}
	if len(decoded.GPU) != 1 {
		t.Fatalf("gpu = %v, want one device", decoded.GPU)
	}
	// A newer node adding a field must survive the round trip, which is the
	// point of keeping the payload opaque.
	if _, ok := decoded.GPU[0]["future_field_we_do_not_know"]; !ok {
		t.Fatalf("unknown gpu field was dropped: %s", lastStats)
	}
}

// A node predating resource sampling — and a node that reports an explicit null
// — must both produce nil, which persists as SQL NULL. Writing an empty object
// instead would draw zeros on a dashboard for a node that measured nothing.
func TestCheckNodeReportsNoStatsForOlderNodes(t *testing.T) {
	for name, body := range map[string]string{
		"fields absent":  `{"status":"ok","active_jobs":1,"egress_kbps":0,"capabilities_hash":""}`,
		"fields null":    `{"status":"ok","active_jobs":1,"system":null,"gpu":null}`,
		"gpu empty only": `{"status":"ok","active_jobs":1,"gpu":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			url := newStatsHealthNode(t, body)
			healthy, _, _, _, lastStats := CheckNode(context.Background(), &Node{URL: url})
			if !healthy {
				t.Fatal("node reported unhealthy")
			}
			if lastStats != nil {
				t.Fatalf("lastStats = %s, want nil so the column is written NULL", lastStats)
			}
		})
	}
}

// A node that reports only one half still persists that half.
func TestCheckNodeKeepsAPartialSample(t *testing.T) {
	url := newStatsHealthNode(t, `{"status":"ok","gpu":[{"device":"cuda:0","source":"nvidia-smi"}]}`)
	_, _, _, _, lastStats := CheckNode(context.Background(), &Node{URL: url})
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(lastStats, &decoded); err != nil {
		t.Fatalf("lastStats invalid: %v (%s)", err, lastStats)
	}
	if _, ok := decoded["system"]; ok {
		t.Fatalf("system emitted when the node sent none: %s", lastStats)
	}
	if _, ok := decoded["gpu"]; !ok {
		t.Fatalf("gpu missing: %s", lastStats)
	}
}

// A node is a worker that may run on hardware this process does not control,
// and last_stats is the one thing it dictates that gets written to the control
// plane database every 30 seconds. An oversized sample is dropped, not stored —
// but whether the node is alive still routes streams, so the verdict survives.
func TestCheckNodeDropsAnOversizedResourceSample(t *testing.T) {
	padding := strings.Repeat("x", maxLastStatsBytes)
	url := newStatsHealthNode(t, `{"status":"ok","active_jobs":3,"egress_kbps":9,
		"system":{"cpu_pct":41,"junk":"`+padding+`"}}`)

	healthy, activeJobs, egressKbps, _, lastStats := CheckNode(context.Background(), &Node{URL: url})
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

	healthy, activeJobs, _, _, lastStats := CheckNode(context.Background(), &Node{URL: url})
	if healthy || activeJobs != 0 || lastStats != nil {
		t.Fatalf("check = %v/%d/%s, want an unreadable body treated as no answer", healthy, activeJobs, lastStats)
	}
}

// An unreachable node reports nothing, and its stats must be cleared rather
// than left behind: a dead node's five-minute-old CPU number reads as live.
func TestApplyHealthClearsStatsWhenACheckCarriesNone(t *testing.T) {
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, URL: "http://node", Enabled: true}})

	pool.ApplyHealth(1, "http://node", true, 1, 0, "", []byte(`{"system":{"cpu_pct":41}}`), time.Now())
	stored := pool.Nodes()[0]
	if len(stored.LastStats) == 0 {
		t.Fatal("stats were not published to the pool")
	}

	pool.ApplyHealth(1, "http://node", false, 0, 0, "", nil, time.Now())
	if got := pool.Nodes()[0].LastStats; got != nil {
		t.Fatalf("LastStats = %s after a failed check, want nil", got)
	}
}

// The pool publishes immutable copies. A caller's buffer (a decoded HTTP body)
// must not stay aliased into a node other goroutines are reading.
func TestApplyHealthClonesStats(t *testing.T) {
	pool := NewProxyPool()
	pool.SetNodes([]*Node{{ID: 1, URL: "http://node", Enabled: true}})

	buffer := []byte(`{"system":{"cpu_pct":41}}`)
	pool.ApplyHealth(1, "http://node", true, 0, 0, "", buffer, time.Now())
	copy(buffer, []byte(`{"system":{"cpu_pct":99}}`))

	if got := string(pool.Nodes()[0].LastStats); got != `{"system":{"cpu_pct":41}}` {
		t.Fatalf("LastStats = %s, want the published copy to be independent of the caller's buffer", got)
	}
}

// The database fence cannot undo a pool write that already happened. A health
// request is bounded at five seconds, which is ample time for an administrator
// to repoint a row and reload the pools; publishing by id alone would then put
// one worker's health — and the scratch fill transcode admission reads — onto
// the replacement, and it would stay there until a later sweep.
func TestApplyHealthIgnoresAResultForAReplacedWorker(t *testing.T) {
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, URL: "http://replacement", Enabled: true}})

	pool.ApplyHealth(1, "http://original", true, 7, 0, "", []byte(`{"system":{"cpu_pct":41}}`), time.Now())

	stored := pool.Nodes()[0]
	if stored.ActiveJobs != 0 || len(stored.LastStats) != 0 || stored.LastHealthCheck != nil {
		t.Fatalf("pool took the old worker's result: %+v", stored)
	}
}

// The fence must not reject the ordinary case: the pools normalize URLs and the
// database column does not, so a trailing slash on one side is the same worker.
func TestApplyHealthAcceptsATrailingSlashDifference(t *testing.T) {
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, URL: "http://node/", Enabled: true}})

	pool.ApplyHealth(1, "http://node", true, 3, 0, "", nil, time.Now())

	if stored := pool.Nodes()[0]; stored.ActiveJobs != 3 {
		t.Fatalf("active jobs = %d, want the result applied despite the trailing slash", stored.ActiveJobs)
	}
}

// Capability reports carry the GPU identities the planner places work on, and
// their fetch is bounded at two minutes — an even wider window for the row to
// be repointed underneath them.
func TestApplyCapabilitiesIgnoresAReportForAReplacedWorker(t *testing.T) {
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, URL: "http://replacement", Enabled: true}})

	pool.ApplyCapabilities(1, "http://original",
		[]byte(`{"resolved":"qsv","render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],"boot_id":"boot-1"}`),
		"sha256:stale", time.Now(), nil, nil)

	stored := pool.Nodes()[0]
	if len(stored.Capabilities) != 0 || stored.CapabilitiesHash != nil || len(stored.PhysicalGPUKeys) != 0 {
		t.Fatalf("pool took the old worker's capability report: %+v", stored)
	}
}

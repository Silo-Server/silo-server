package jellycompat

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// compatTimeoutPlanner is the smallest planner that lets
// toneMapCapabilityTimeout enumerate and resolve pooled nodes.
type compatTimeoutPlanner struct {
	transcodes []string
	proxies    []string
	nodes      map[string]*nodepool.Node
	proxyNodes map[string]*nodepool.Node
}

func (p compatTimeoutPlanner) PlanSession(string, string, bool, int) nodepool.Plan {
	return nodepool.Plan{}
}

func (p compatTimeoutPlanner) TranscodeNodeURLs() []string { return p.transcodes }

func (p compatTimeoutPlanner) ProxyNodeURLs() []string { return p.proxies }

func (p compatTimeoutPlanner) TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool) {
	node, ok := p.nodes[nodeURL]
	return node, ok
}

func (p compatTimeoutPlanner) ProxyNodeByURL(nodeURL string) (*nodepool.Node, bool) {
	node, ok := p.proxyNodes[nodeURL]
	return node, ok
}

// The capability sweep wraps one deadline around concurrent fetches of every
// pooled node, so the budget must cover the slowest node in the fan-out. A
// node whose stored report advertises a probe budget past the fixed fallback
// used to be canceled mid-probe while still inside that budget, and HDR or
// audio-boost planning then excluded it.
func TestToneMapCapabilityTimeoutCoversTheSlowestPooledNode(t *testing.T) {
	advertisedMillis := (4 * time.Minute).Milliseconds()
	report := json.RawMessage(fmt.Sprintf(`{"probe_request_timeout_ms":%d}`, advertisedMillis))

	// Derivation-guard rather than a constant: the expected budget is what the
	// shared pricing rule answers for this report, asserted to actually exceed
	// the fallback so the test cannot pass vacuously if the fixture stops
	// out-pricing it.
	want := playback.ColdCapabilityRequestTimeout(report, "", "", compatRemoteNodeProbeFallbackTimeout)
	if want <= compatRemoteNodeProbeFallbackTimeout {
		t.Fatalf("fixture no longer out-prices the fallback: got %v, fallback %v", want, compatRemoteNodeProbeFallbackTimeout)
	}

	handler := &PlaybackHandler{
		NodePlanner: compatTimeoutPlanner{
			transcodes: []string{"http://cheap:8082", "http://slow:8082"},
			nodes: map[string]*nodepool.Node{
				// The cheap node advertises nothing; only the slow one raises
				// the sweep, which is what makes the answer a maximum.
				"http://cheap:8082": {URL: "http://cheap:8082"},
				"http://slow:8082":  {URL: "http://slow:8082", Capabilities: report},
			},
		},
	}

	if got := handler.toneMapCapabilityTimeout(); got != want {
		t.Fatalf("toneMapCapabilityTimeout() = %v, want the slowest node's cold budget %v", got, want)
	}
}

// A node's own acceleration override prices its probe walk even before any
// refetch stores a report for it — the moment the override is saved is exactly
// when the stored figure is most wrong.
func TestToneMapCapabilityTimeoutPricesANodeOverride(t *testing.T) {
	node := &nodepool.Node{URL: "http://wide:8082"}
	accel := "qsv"
	devices := "/dev/dri/renderD128,/dev/dri/renderD129,/dev/dri/renderD130,/dev/dri/renderD131"
	node.HWAccelOverride = &accel
	node.HWDeviceOverride = &devices

	want := playback.ColdCapabilityRequestTimeout(nil, accel, devices, compatRemoteNodeProbeFallbackTimeout)

	handler := &PlaybackHandler{
		NodePlanner: compatTimeoutPlanner{
			transcodes: []string{node.URL},
			nodes:      map[string]*nodepool.Node{node.URL: node},
		},
	}

	if got := handler.toneMapCapabilityTimeout(); got != want {
		t.Fatalf("toneMapCapabilityTimeout() = %v, want the override-priced budget %v", got, want)
	}
}

// A planner that cannot enumerate nodes — or none at all — keeps the
// pre-derivation behavior: the cluster policy over the fixed fallback.
func TestToneMapCapabilityTimeoutFallsBackWithoutAPool(t *testing.T) {
	want := playback.ColdCapabilityRequestTimeout(nil, "", "", compatRemoteNodeProbeFallbackTimeout)

	handler := &PlaybackHandler{cfg: &config.Config{}}
	if got := handler.toneMapCapabilityTimeout(); got != want {
		t.Fatalf("toneMapCapabilityTimeout() with no planner = %v, want %v", got, want)
	}
}

// Proxy nodes answer the audio-boost sweep under the same deadline, resolved
// through their own pool: a proxy whose stored report out-prices the fallback
// raises the sweep exactly as a transcode node's would.
func TestToneMapCapabilityTimeoutPricesProxyNodesFromTheirRecords(t *testing.T) {
	advertisedMillis := (3 * time.Minute).Milliseconds()
	report := json.RawMessage(fmt.Sprintf(`{"probe_request_timeout_ms":%d}`, advertisedMillis))

	want := playback.ColdCapabilityRequestTimeout(report, "", "", compatRemoteNodeProbeFallbackTimeout)
	if want <= compatRemoteNodeProbeFallbackTimeout {
		t.Fatalf("fixture no longer out-prices the fallback: got %v, fallback %v", want, compatRemoteNodeProbeFallbackTimeout)
	}

	handler := &PlaybackHandler{
		NodePlanner: compatTimeoutPlanner{
			proxies: []string{"http://proxy:8083"},
			proxyNodes: map[string]*nodepool.Node{
				"http://proxy:8083": {URL: "http://proxy:8083", Capabilities: report},
			},
		},
	}
	if got := handler.toneMapCapabilityTimeout(); got != want {
		t.Fatalf("toneMapCapabilityTimeout() = %v, want the proxy's cold budget %v", got, want)
	}
}

// A pooled proxy the lookup cannot resolve still prices at the floor instead
// of shrinking or failing the sweep.
func TestToneMapCapabilityTimeoutCountsUnresolvedProxyNodesAtTheFloor(t *testing.T) {
	want := playback.ColdCapabilityRequestTimeout(nil, "", "", compatRemoteNodeProbeFallbackTimeout)

	handler := &PlaybackHandler{
		NodePlanner: compatTimeoutPlanner{proxies: []string{"http://proxy:8083"}},
	}
	if got := handler.toneMapCapabilityTimeout(); got != want {
		t.Fatalf("toneMapCapabilityTimeout() with an unresolved proxy = %v, want %v", got, want)
	}
}

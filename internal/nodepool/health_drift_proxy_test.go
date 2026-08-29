package nodepool

import (
	"context"
	"encoding/json"
	"testing"
)

// newProxyCapabilityFixture wires one proxy node into a checker with a fake
// fetcher, the proxy-pool mirror of newCapabilityCheckerFixture.
func newProxyCapabilityFixture(t *testing.T, node *Node, fetcher *fakeCapabilityFetcher) (*HealthChecker, *ProxyPool) {
	t.Helper()
	proxyPool := NewProxyPool()
	proxyPool.SetNodes([]*Node{node})
	checker := NewHealthChecker(proxyPool, NewTranscodePool(), nil)
	checker.SetCapabilityFetcher(fetcher.fetch)
	return checker, proxyPool
}

// proxyCapabilityPayload is what a proxy reports now: what its ffmpeg can do,
// and no hardware inventory at all.
const proxyCapabilityPayload = `{"resolved":"none","source":"local",` +
	`"transformations":[{"name":"audio_to_aac","recipe_version":"2"}],` +
	`"capability_hash":"sha256:proxy-new"}`

// A proxy never executes a hardware transcode, so its report deliberately
// carries no backends and no render devices. Comparing one against a report an
// older build stored — which walked the host and listed its GPU — finds every
// device gone at once, and nothing could ever clear that note: recovery is
// evidenced by probes this proxy will never run again, so hardwareProbesEvidenced
// is false forever. Drift is a statement about transcode hardware; on a proxy it
// must not be computed at all, and the upgrade must erase what an older build
// latched rather than freeze it.
func TestProxyCapabilityRefreshNeverLatchesDrift(t *testing.T) {
	const previousProxyPayload = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"detected_backends":[{"backend":"qsv","verified":true}],"capability_hash":"sha256:proxy-old"}`
	stale := "render devices gone: /dev/dri/renderD128"
	url := newHealthNode(t, "sha256:proxy-new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(proxyCapabilityPayload), hash: "sha256:proxy-new"}
	node := &Node{
		ID: 1, Name: "proxy-1", Type: NodeTypeProxy, URL: url, Enabled: true,
		Capabilities:            json.RawMessage(previousProxyPayload),
		CapabilitiesHash:        stringPtr("sha256:proxy-old"),
		CapabilityDrift:         &stale,
		CapabilityDriftBaseline: json.RawMessage(`{"devices":[{"aliases":["/dev/dri/renderD128"]}]}`),
	}
	checker, pool := newProxyCapabilityFixture(t, node, fetcher)

	checker.checkAll(context.Background())
	checker.waitForCapabilityRefreshes()

	nodes := pool.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("pool holds %d nodes, want 1", len(nodes))
	}
	stored := nodes[0]
	if stored.CapabilityDrift != nil {
		t.Fatalf("capability_drift = %q on a proxy, want nothing: a proxy reports no hardware to lose",
			*stored.CapabilityDrift)
	}
	if len(stored.CapabilityDriftBaseline) != 0 {
		t.Fatalf("capability_drift_baseline = %s on a proxy, want it cleared", stored.CapabilityDriftBaseline)
	}
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:proxy-new" {
		t.Fatalf("capabilities_hash = %v, want the fetched report to have landed", stored.CapabilitiesHash)
	}
}

package nodepool

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCapabilityFetcher records what the sweep asked for and answers with a
// canned report, standing in for a real node's /hw-capabilities.
type fakeCapabilityFetcher struct {
	mu      sync.Mutex
	calls   []string
	payload []byte
	hash    string
	err     error
}

func (f *fakeCapabilityFetcher) fetch(_ context.Context, node *Node) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, node.URL)
	return f.payload, f.hash, f.err
}

func (f *fakeCapabilityFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newHealthNode starts a stand-in node whose /health advertises capabilitiesHash.
func newHealthNode(t *testing.T, capabilitiesHash string) string {
	t.Helper()
	body, err := json.Marshal(healthResponse{Status: "ok", ActiveJobs: 2, EgressKbps: 17, CapabilitiesHash: capabilitiesHash, NodeControlVersion: NodeControlVersion})
	if err != nil {
		t.Fatal(err)
	}
	return newStatsHealthNode(t, string(body))
}

// capabilityCheckerFixture wires one transcode node into a checker with a fake
// fetcher and a callback recorder, which is the whole capability flow minus the
// database (repo stays nil, as it is in proxy/transcode modes).
type capabilityCheckerFixture struct {
	checker *HealthChecker
	pool    *TranscodePool
	fetcher *fakeCapabilityFetcher
	node    *Node

	mu       sync.Mutex
	notified []string
}

func newCapabilityCheckerFixture(t *testing.T, node *Node, fetcher *fakeCapabilityFetcher) *capabilityCheckerFixture {
	t.Helper()
	transcodePool := newKeyedTranscodePool()
	transcodePool.SetNodes([]*Node{node})
	fixture := &capabilityCheckerFixture{
		checker: NewHealthChecker(NewProxyPool(), transcodePool, nil),
		pool:    transcodePool,
		fetcher: fetcher,
		node:    node,
	}
	fixture.checker.SetCapabilityFetcher(fetcher.fetch)
	fixture.checker.SetCapabilitiesChangedCallback(func(nodeURL string) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.notified = append(fixture.notified, nodeURL)
	})
	return fixture
}

// sweep runs one health sweep and then waits for the capability fetches it
// detached, which is what a test needs to observe. Production deliberately does
// not wait: see startCapabilityRefresh.
func (f *capabilityCheckerFixture) sweep() {
	f.checker.checkAll(context.Background())
	f.checker.waitForCapabilityRefreshes()
}

func (f *capabilityCheckerFixture) notifications() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notified...)
}

// storedNode returns the pool's current copy, which is what the next sweep
// compares against.
func (f *capabilityCheckerFixture) storedNode(t *testing.T) *Node {
	t.Helper()
	nodes := f.pool.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("pool holds %d nodes, want 1", len(nodes))
	}
	return nodes[0]
}

const testCapabilityPayload = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
	`"detected_backends":[{"backend":"nvenc","verified":true}],"capability_hash":"sha256:new"}`

// Nothing changed: the sweep must cost one health request, not a probe of every
// node every 30 seconds.
func TestHealthCheckerSkipsFetchWhenHashUnchanged(t *testing.T) {
	url := newHealthNode(t, "sha256:same")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:same"}
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", URL: url, Enabled: true,
		Capabilities:     json.RawMessage(testCapabilityPayload),
		CapabilitiesHash: strPtr("sha256:same"),
	}, fetcher)

	fixture.sweep()

	if got := fetcher.callCount(); got != 0 {
		t.Fatalf("capability fetches = %d, want none for an unchanged hash", got)
	}
	if got := fixture.notifications(); len(got) != 0 {
		t.Fatalf("notifications = %v, want none", got)
	}
}

// A failed fetch is not evidence about the hardware: keep what is stored and
// retry on the next sweep.
func TestHealthCheckerKeepsStoredCapabilitiesOnFetchFailure(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{err: errors.New("connection refused")}
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", URL: url, Enabled: true,
		Capabilities:     json.RawMessage(testCapabilityPayload),
		CapabilitiesHash: strPtr("sha256:old"),
	}, fetcher)

	fixture.sweep()
	stored := fixture.storedNode(t)
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:old" {
		t.Fatalf("stored hash = %v, want the previous report kept", stored.CapabilitiesHash)
	}
	if got := fixture.notifications(); len(got) != 0 {
		t.Fatalf("notifications = %v, want none after a failed fetch", got)
	}

	// The next sweep retries rather than backing off forever.
	fixture.sweep()
	if got := fetcher.callCount(); got != 2 {
		t.Fatalf("capability fetches = %d, want a retry on the next sweep", got)
	}
}

// A payload without its own hash cannot be tracked for change; storing it would
// refetch every sweep forever.
func TestHealthCheckerRefusesUnhashedCapabilityPayload(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(`{"resolved":"nvenc"}`), hash: ""}
	fixture := newCapabilityCheckerFixture(t, &Node{ID: 1, Name: "gpu-1", URL: url, Enabled: true}, fetcher)

	fixture.sweep()

	stored := fixture.storedNode(t)
	if stored.CapabilitiesHash != nil || stored.Capabilities != nil {
		t.Fatalf("unhashed payload was stored: hash=%v payload=%s", stored.CapabilitiesHash, stored.Capabilities)
	}
	if got := fixture.notifications(); len(got) != 0 {
		t.Fatalf("notifications = %v, want none", got)
	}
}

// Proxy nodes carry capabilities too (they execute remux recipes), so the sweep
// must publish through the proxy pool's copy-on-write path as well.
func TestHealthCheckerAppliesCapabilitiesToProxyPool(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	proxyPool := NewProxyPool()
	proxyPool.SetNodes([]*Node{{ID: 9, Name: "proxy-1", URL: url, Enabled: true}})
	checker := NewHealthChecker(proxyPool, newKeyedTranscodePool(), nil)
	checker.SetCapabilityFetcher(fetcher.fetch)

	checker.checkAll(context.Background())
	checker.waitForCapabilityRefreshes()

	stored := proxyPool.Nodes()[0]
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:new" {
		t.Fatalf("proxy pool hash = %v, want sha256:new", stored.CapabilitiesHash)
	}
}

// blockedSweepTimeout only bounds a failure: on a correct sweep the wait below
// is satisfied by the sweep returning, not by elapsed time.
const blockedSweepTimeout = 30 * time.Second

// blockingFetcher answers only after it is released, so a test can hold one
// node's capability fetch open and observe what the sweep does meanwhile.
type blockingFetcher struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func newBlockingFetcher() *blockingFetcher {
	return &blockingFetcher{started: make(chan struct{}, 1), release: make(chan struct{})}
}

func (f *blockingFetcher) fetch(ctx context.Context, _ *Node) ([]byte, string, error) {
	f.calls.Add(1)
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return []byte(testCapabilityPayload), "sha256:new", nil
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
}

// The capability fetch budget is larger than the sweep interval, so a fetch
// that ran inside the sweep would stretch every other node's liveness update
// past that interval — and pool health is what routes streams away from a node
// that just died. The sweep must return while the fetch is still outstanding.
func TestHealthCheckerSweepDoesNotWaitForCapabilityFetch(t *testing.T) {
	slowURL := newHealthNode(t, "sha256:new")
	// The second node advertises no hash, so its own health is all the sweep
	// has to do for it.
	steadyURL := newHealthNode(t, "")
	pool := newKeyedTranscodePool()
	pool.SetNodes([]*Node{
		{ID: 1, Name: "slow-gpu", URL: slowURL, Enabled: true},
		{ID: 2, Name: "steady-gpu", URL: steadyURL, Enabled: true},
	})
	checker := NewHealthChecker(NewProxyPool(), pool, nil)
	fetcher := newBlockingFetcher()
	checker.SetCapabilityFetcher(fetcher.fetch)

	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		checker.checkAll(context.Background())
	}()

	<-fetcher.started
	select {
	case <-sweepDone:
	case <-time.After(blockedSweepTimeout):
		t.Fatal("sweep is still running while one node's capability fetch is outstanding")
	}

	for _, n := range pool.Nodes() {
		if !n.Healthy || n.LastHealthCheck == nil {
			t.Fatalf("node %s health was not applied during the blocked fetch: %+v", n.Name, n)
		}
	}

	close(fetcher.release)
	checker.waitForCapabilityRefreshes()
	stored := pool.Nodes()[0]
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:new" {
		t.Fatalf("detached fetch did not publish its report: hash = %v", stored.CapabilitiesHash)
	}
}

// A fetch that outlives its sweep has not moved the stored hash, so the next
// sweep sees the same mismatch. Without a per-node guard that would start a
// second fetch of the same node on every sweep until the first one answers.
func TestHealthCheckerDoesNotStackCapabilityFetchesForOneNode(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	pool := newKeyedTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, Name: "slow-gpu", URL: url, Enabled: true}})
	checker := NewHealthChecker(NewProxyPool(), pool, nil)
	fetcher := newBlockingFetcher()
	checker.SetCapabilityFetcher(fetcher.fetch)

	checker.checkAll(context.Background())
	<-fetcher.started
	checker.checkAll(context.Background())

	if got := fetcher.calls.Load(); got != 1 {
		t.Fatalf("capability fetches = %d while the first is still outstanding, want 1", got)
	}

	close(fetcher.release)
	checker.waitForCapabilityRefreshes()

	// Once the report is stored the hash matches, so no further fetch is due.
	checker.checkAll(context.Background())
	checker.waitForCapabilityRefreshes()
	if got := fetcher.calls.Load(); got != 1 {
		t.Fatalf("capability fetches = %d after the report was stored, want 1", got)
	}
}

package nodepool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(healthResponse{
			Status:           "ok",
			ActiveJobs:       2,
			EgressKbps:       17,
			CapabilitiesHash: capabilitiesHash,
		})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func stringPtr(s string) *string { return &s }

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
	transcodePool := NewTranscodePool()
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

// A node whose hardware changed advertises a new hash; the sweep must fetch the
// report once, publish it to the pool, and tell the capability cache to drop
// what it had.
func TestHealthCheckerFetchesCapabilitiesOnHashChange(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	fixture := newCapabilityCheckerFixture(t, &Node{ID: 1, Name: "gpu-1", URL: url, Enabled: true}, fetcher)

	fixture.sweep()

	if got := fetcher.callCount(); got != 1 {
		t.Fatalf("capability fetches = %d, want exactly 1 per sweep", got)
	}
	stored := fixture.storedNode(t)
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:new" {
		t.Fatalf("pool hash = %v, want sha256:new", stored.CapabilitiesHash)
	}
	if string(stored.Capabilities) != testCapabilityPayload {
		t.Fatalf("pool payload = %s", stored.Capabilities)
	}
	if stored.CapabilitiesRefreshedAt == nil || stored.CapabilitiesRefreshedAt.IsZero() {
		t.Fatal("pool copy carries no capability refresh time")
	}
	if got := fixture.notifications(); len(got) != 1 || got[0] != url {
		t.Fatalf("capability change notifications = %v, want one for %s", got, url)
	}
}

// Nothing changed: the sweep must cost one health request, not a probe of every
// node every 30 seconds.
func TestHealthCheckerSkipsFetchWhenHashUnchanged(t *testing.T) {
	url := newHealthNode(t, "sha256:same")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:same"}
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", URL: url, Enabled: true,
		Capabilities:     json.RawMessage(testCapabilityPayload),
		CapabilitiesHash: stringPtr("sha256:same"),
	}, fetcher)

	fixture.sweep()

	if got := fetcher.callCount(); got != 0 {
		t.Fatalf("capability fetches = %d, want none for an unchanged hash", got)
	}
	if got := fixture.notifications(); len(got) != 0 {
		t.Fatalf("notifications = %v, want none", got)
	}
}

// A node from before capability snapshots reports no hash. It gets exactly the
// old behavior: no fetch, and nothing invented on its behalf.
func TestHealthCheckerSkipsFetchWhenNodeReportsNoHash(t *testing.T) {
	url := newHealthNode(t, "")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	fixture := newCapabilityCheckerFixture(t, &Node{ID: 1, Name: "old-node", URL: url, Enabled: true}, fetcher)

	fixture.sweep()

	if got := fetcher.callCount(); got != 0 {
		t.Fatalf("capability fetches = %d, want none for a node that reports no hash", got)
	}
	stored := fixture.storedNode(t)
	if stored.CapabilitiesHash != nil || stored.Capabilities != nil {
		t.Fatalf("stored capabilities were synthesized: hash=%v payload=%s", stored.CapabilitiesHash, stored.Capabilities)
	}
	if !stored.Healthy {
		t.Fatal("node without a capability hash was not marked healthy")
	}
}

// A node that is down tells us nothing about its hardware, so its stored
// inventory must survive rather than be cleared or refetched.
func TestHealthCheckerSkipsFetchWhenNodeUnhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", URL: server.URL, Enabled: true, Healthy: true,
		Capabilities:     json.RawMessage(testCapabilityPayload),
		CapabilitiesHash: stringPtr("sha256:old"),
	}, fetcher)

	fixture.sweep()

	if got := fetcher.callCount(); got != 0 {
		t.Fatalf("capability fetches = %d, want none for an unhealthy node", got)
	}
	stored := fixture.storedNode(t)
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:old" {
		t.Fatalf("stored hash = %v, want the previous report kept", stored.CapabilitiesHash)
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
		CapabilitiesHash: stringPtr("sha256:old"),
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
	checker := NewHealthChecker(proxyPool, NewTranscodePool(), nil)
	checker.SetCapabilityFetcher(fetcher.fetch)

	checker.checkAll(context.Background())
	checker.waitForCapabilityRefreshes()

	stored := proxyPool.Nodes()[0]
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:new" {
		t.Fatalf("proxy pool hash = %v, want sha256:new", stored.CapabilitiesHash)
	}
}

// Without a fetcher wired (proxy/transcode modes, or before wiring) the sweep
// must behave exactly as it did before capability tracking.
func TestHealthCheckerWithoutFetcherStillChecksHealth(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, Name: "gpu-1", URL: url, Enabled: true}})
	checker := NewHealthChecker(NewProxyPool(), pool, nil)

	checker.checkAll(context.Background())

	stored := pool.Nodes()[0]
	if !stored.Healthy || stored.ActiveJobs != 2 || stored.EgressKbps != 17 {
		t.Fatalf("health was not applied: %+v", stored)
	}
	if stored.Capabilities != nil {
		t.Fatalf("capabilities = %s, want none without a fetcher", stored.Capabilities)
	}
}

// Losing a verified backend or a render device is the case worth warning about:
// the node still answers health, so nothing else would surface the loss.
func TestHealthCheckerWarnsOnCapabilityDrift(t *testing.T) {
	url := newHealthNode(t, "sha256:degraded")
	degraded := `{"resolved":"none","render_devices":[],` +
		`"detected_backends":[{"backend":"nvenc","verified":false}],"capability_hash":"sha256:degraded"}`
	fetcher := &fakeCapabilityFetcher{payload: []byte(degraded), hash: "sha256:degraded"}
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", URL: url, Enabled: true,
		Capabilities:     json.RawMessage(testCapabilityPayload),
		CapabilitiesHash: stringPtr("sha256:old"),
	}, fetcher)

	logged := captureSlog(t, slog.LevelWarn)
	fixture.sweep()

	output := logged.String()
	if !bytes.Contains(logged.Bytes(), []byte("node capability drift")) {
		t.Fatalf("no drift warning logged; log was:\n%s", output)
	}
	for _, want := range []string{"nvenc", "/dev/dri/renderD128"} {
		if !bytes.Contains(logged.Bytes(), []byte(want)) {
			t.Fatalf("drift warning does not name %q; log was:\n%s", want, output)
		}
	}
}

// The first report is not drift — there is nothing to compare it against — so
// it must not warn.
func TestHealthCheckerDoesNotWarnOnFirstCapabilityStore(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	fixture := newCapabilityCheckerFixture(t, &Node{ID: 1, Name: "gpu-1", URL: url, Enabled: true}, fetcher)

	logged := captureSlog(t, slog.LevelInfo)
	fixture.sweep()

	if bytes.Contains(logged.Bytes(), []byte("node capability drift")) {
		t.Fatalf("first capability store logged drift:\n%s", logged.String())
	}
	if !bytes.Contains(logged.Bytes(), []byte("node capabilities stored")) {
		t.Fatalf("first capability store was not logged:\n%s", logged.String())
	}
}

// Gaining hardware is not drift either: only a loss is worth an operator's
// attention.
func TestHealthCheckerDoesNotWarnWhenCapabilitiesImprove(t *testing.T) {
	url := newHealthNode(t, "sha256:better")
	improved := `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128","/dev/dri/renderD129"],` +
		`"detected_backends":[{"backend":"nvenc","verified":true},{"backend":"vaapi","verified":true}],` +
		`"capability_hash":"sha256:better"}`
	fetcher := &fakeCapabilityFetcher{payload: []byte(improved), hash: "sha256:better"}
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", URL: url, Enabled: true,
		Capabilities:     json.RawMessage(testCapabilityPayload),
		CapabilitiesHash: stringPtr("sha256:old"),
	}, fetcher)

	logged := captureSlog(t, slog.LevelWarn)
	fixture.sweep()

	if bytes.Contains(logged.Bytes(), []byte("node capability drift")) {
		t.Fatalf("added hardware logged as drift:\n%s", logged.String())
	}
}

// captureSlog redirects the default logger for one test and returns its output.
func captureSlog(t *testing.T, level slog.Level) *lockedBuffer {
	t.Helper()
	buffer := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}

// lockedBuffer collects log output written from the sweep's per-node goroutines.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *lockedBuffer) String() string { return string(b.Bytes()) }

// The fetch budget must not inherit the 5s health timeout: a cold node runs
// ffmpeg probes to answer a capability request.
func TestCapabilityFetchTimeoutExceedsHealthTimeout(t *testing.T) {
	if capabilityFetchTimeout <= 5*time.Second {
		t.Fatalf("capabilityFetchTimeout = %s, want more than the health request budget", capabilityFetchTimeout)
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
	pool := NewTranscodePool()
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
	pool := NewTranscodePool()
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

// The probe matrix a cold node runs grows with its device count without bound —
// nine render devices legitimately ask for over five minutes — so a fixed outer
// deadline cancels a node operating inside its published contract, and its
// inventory never populates. The backstop is derived from the budget the fetcher
// gave itself so the fetcher's own deadline always fires first.
func TestCapabilityFetchBackstopSitsAboveTheFetcherBudget(t *testing.T) {
	checker := NewHealthChecker(NewProxyPool(), NewTranscodePool(), nil)
	node := &Node{ID: 1, URL: "http://gpu-1"}

	if got := checker.capabilityFetchBackstop(node); got != capabilityFetchTimeout {
		t.Fatalf("backstop with no budget wired = %v, want the %v floor", got, capabilityFetchTimeout)
	}

	// A budget under the floor leaves the floor standing: it already clears the
	// fetcher by a wide margin.
	checker.SetCapabilityFetchBudget(func(*Node) time.Duration { return 2 * time.Minute })
	if got := checker.capabilityFetchBackstop(node); got != capabilityFetchTimeout {
		t.Fatalf("backstop for a small budget = %v, want the %v floor", got, capabilityFetchTimeout)
	}

	// A budget above it carries the backstop with it, always by the slack, so
	// the fetch is never cut short by this.
	big := 311 * time.Second
	checker.SetCapabilityFetchBudget(func(*Node) time.Duration { return big })
	got := checker.capabilityFetchBackstop(node)
	if got != big+capabilityFetchSlack {
		t.Fatalf("backstop for a %v budget = %v, want %v", big, got, big+capabilityFetchSlack)
	}
	if got <= big {
		t.Fatalf("backstop %v does not clear the %v the fetcher allowed itself", got, big)
	}
}

// A stored node URL may carry a trailing slash — pasting a base URL is the usual
// way an operator enters one, and everything here already treats the two forms
// as the same worker. Concatenating a route onto it produces "//admin/…", which
// no node's router has: the request 404s against a node that is running and
// reachable, and the operator's action fails for a reason nothing reports.
func TestNodeEndpointJoinsATrailingSlashBaseURL(t *testing.T) {
	const want = "http://gpu-1:8082/admin/reprobe-capabilities"
	for _, base := range []string{
		"http://gpu-1:8082",
		"http://gpu-1:8082/",
		"http://gpu-1:8082///",
	} {
		if got := NodeEndpoint(base, "/admin/reprobe-capabilities"); got != want {
			t.Errorf("NodeEndpoint(%q) = %q, want %q", base, got, want)
		}
	}
}

// The health check is the first thing that runs against a node, so a base URL
// this could not address would leave the node permanently unhealthy.
func TestCheckNodeReachesATrailingSlashBaseURL(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"active_jobs": 1, "capabilities_hash": "sha256:x"})
	}))
	defer node.Close()

	healthy, activeJobs, _, hash, _ := CheckNode(context.Background(), &Node{ID: 1, URL: node.URL + "/"})
	if !healthy {
		t.Fatal("a node stored with a trailing slash was reported unhealthy")
	}
	if activeJobs != 1 || hash != "sha256:x" {
		t.Fatalf("active jobs = %d, hash = %q; want the node's own answer", activeJobs, hash)
	}
}

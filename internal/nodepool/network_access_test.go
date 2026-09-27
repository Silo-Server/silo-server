package nodepool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// ClientURLFor is the one accessor every client-facing proxy URL is built on.
// The default path keeps ClientURL's public-over-backend rule; a provider path
// gets exactly that provider's connected origin, and nothing else — never the
// LAN address, never another provider's origin, never a non-connected one.
func TestNodeClientURLFor(t *testing.T) {
	public := "https://cdn.example.com/"
	node := &Node{
		URL: "http://10.0.0.9:8083/", PublicURL: &public,
		NetworkAccess: netaccess.NodeNetworkAccess{
			"tailscale": {State: netaccess.StateConnected, Origin: "https://proxy-1.tail1234.ts.net/"},
			"netbird":   {State: netaccess.StateConnecting, Origin: "https://proxy-1.netbird.example"},
			"garbage":   {State: netaccess.StateConnected, Origin: "::not-an-origin"},
		},
	}
	noPublic := &Node{URL: "http://10.0.0.9:8083/"}
	for _, tc := range []struct {
		name string
		node *Node
		path netaccess.Path
		want string
	}{
		{"default path uses the public URL", node, netaccess.Path{}, "https://cdn.example.com"},
		{"default path falls back to the backend URL", noPublic, netaccess.Path{}, "http://10.0.0.9:8083"},
		{"connected provider origin", node, netaccess.Path{Provider: "tailscale"}, "https://proxy-1.tail1234.ts.net"},
		{"connecting provider has no origin", node, netaccess.Path{Provider: "netbird"}, ""},
		{"malformed origin is no origin", node, netaccess.Path{Provider: "garbage"}, ""},
		{"unknown provider", node, netaccess.Path{Provider: "zerotier"}, ""},
		{"node without any report", noPublic, netaccess.Path{Provider: "tailscale"}, ""},
		{"nil node", nil, netaccess.Path{Provider: "tailscale"}, ""},
		{"nil node default path", nil, netaccess.Path{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.node.ClientURLFor(tc.path); got != tc.want {
				t.Fatalf("ClientURLFor(%+v) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
	if got := node.ClientURLFor(netaccess.Path{}); got != node.ClientURL() {
		t.Fatalf("default path = %q, ClientURL() = %q; they must agree", got, node.ClientURL())
	}
}

func TestClientReachableVia(t *testing.T) {
	reachable := &Node{ID: 1, URL: "http://lan-1", NetworkAccess: netaccess.NodeNetworkAccess{"tailscale": {State: netaccess.StateConnected, Origin: "https://p1.ts.net"}}}
	lanOnly := &Node{ID: 2, URL: "http://lan-2"}
	tailnet := netaccess.Path{Provider: "tailscale"}

	if got := ClientReachableVia(netaccess.Path{}, nil); got != nil {
		t.Fatal("default path with no base predicate must stay nil so planners accept any healthy proxy")
	}
	calls := 0
	base := func(n *Node) bool { calls++; return n.ID == 2 }
	wrapped := ClientReachableVia(tailnet, base)
	if wrapped(reachable) {
		t.Fatal("base rejected the reachable proxy but the wrapper accepted it")
	}
	if wrapped(lanOnly) {
		t.Fatal("a LAN-only proxy passed on a provider path")
	}
	if wrapped(nil) {
		t.Fatal("nil node accepted")
	}
	if calls != 1 {
		t.Fatalf("base predicate ran %d times, want once (only for the reachable proxy)", calls)
	}
	if !ClientReachableVia(tailnet, nil)(reachable) {
		t.Fatal("reachable proxy rejected with no base predicate")
	}
	if got := ClientReachableVia(netaccess.Path{}, base); got == nil || !got(lanOnly) {
		t.Fatal("default path must hand back the base predicate unchanged")
	}
}

// newNetworkAccessHealthNode serves a health body whose network_access block
// the test can swap between sweeps.
func newNetworkAccessHealthNode(t *testing.T) (string, *atomic.Pointer[string]) {
	t.Helper()
	var block atomic.Pointer[string]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			http.NotFound(w, r)
			return
		}
		body := `{"status":"ok","active_jobs":1`
		if extra := block.Load(); extra != nil && *extra != "" {
			body += `,"network_access":` + *extra
		}
		body += `}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL, &block
}

// The sweep is the only path that learns a proxy's overlay origins. It has to
// publish them to the pool copy the planner reads, and a later check that
// carries none has to clear them again — an origin nobody confirms any more
// must not keep being handed to overlay clients.
func TestHealthCheckerStoresNetworkAccessFromTheHealthPull(t *testing.T) {
	url, block := newNetworkAccessHealthNode(t)
	report := `{" tailscale ":{"state":"connected","origin":" https://proxy-1.tail1234.ts.net ","hostname":"proxy-1.tail1234.ts.net","updated_at":"2026-09-14T08:00:00Z"},"netbird":{"state":"awaiting_authorization"}}`
	block.Store(&report)
	pool := NewProxyPool()
	pool.SetNodes([]*Node{{ID: 9, Name: "proxy-1", URL: url, Enabled: true}})
	checker := NewHealthChecker(pool, NewTranscodePool(), nil)

	checker.checkAll(context.Background())

	stored := pool.Nodes()[0]
	if !stored.Healthy {
		t.Fatalf("node not healthy: %+v", stored)
	}
	if got := stored.ClientURLFor(netaccess.Path{Provider: "tailscale"}); got != "https://proxy-1.tail1234.ts.net" {
		t.Fatalf("tailscale origin after sweep = %q (report %#v)", got, stored.NetworkAccess)
	}
	if got := stored.NetworkAccess["tailscale"]; got.Hostname != "proxy-1.tail1234.ts.net" || got.UpdatedAt.IsZero() {
		t.Fatalf("stored entry lost fields: %#v", got)
	}
	if got := stored.ClientURLFor(netaccess.Path{Provider: "netbird"}); got != "" {
		t.Fatalf("an unauthorized provider yielded %q", got)
	}
	if got := stored.ClientURLFor(netaccess.Path{}); got != NormalizeNodeURL(url) {
		t.Fatalf("default path = %q, want the backend URL", got)
	}

	// A build that predates the field, or one whose providers all stopped,
	// answers without the block: the stored report is cleared, not kept.
	empty := ""
	block.Store(&empty)
	checker.checkAll(context.Background())
	stored = pool.Nodes()[0]
	if !stored.Healthy || stored.NetworkAccess != nil {
		t.Fatalf("after a check without the field: healthy=%v report=%#v, want healthy and no report", stored.Healthy, stored.NetworkAccess)
	}
	if got := stored.ClientURLFor(netaccess.Path{Provider: "tailscale"}); got != "" {
		t.Fatalf("stale origin survived a report-less check: %q", got)
	}
}

func TestCheckNodeIgnoresAMalformedNetworkAccessBlock(t *testing.T) {
	url, block := newNetworkAccessHealthNode(t)
	bad := `"connected"`
	block.Store(&bad)
	healthy, _, _, _, _, report := CheckNode(context.Background(), &Node{URL: url})
	// A body that does not decode is the existing "node did not answer"
	// verdict; the point here is that the block cannot smuggle in a report.
	if healthy || report != nil {
		t.Fatalf("healthy=%v report=%#v, want the undecodable body rejected whole", healthy, report)
	}
}

// The health update is the only write path for network_access, and it has to
// round-trip through the ordinary node read the pools and the admin API use.
func TestRepositoryUpdateHealthPersistsNetworkAccess(t *testing.T) {
	repo := newNetworkAccessTestRepo(t)
	ctx := context.Background()
	unique := time.Now().UnixNano()
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("network-access-test-%d", unique),
		Type: NodeTypeProxy,
		URL:  fmt.Sprintf("http://network-access-test-%d", unique),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	if node.NetworkAccess != nil {
		t.Fatalf("new node already carries a report: %#v", node.NetworkAccess)
	}

	at := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	report := netaccess.NodeNetworkAccess{"tailscale": {State: netaccess.StateConnected, Origin: "https://proxy-1.tail1234.ts.net", Hostname: "proxy-1.tail1234.ts.net", UpdatedAt: at}}
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 1, 0, nil, report); err != nil {
		t.Fatalf("update health: %v", err)
	}
	reloaded, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.NetworkAccess["tailscale"]
	if !ok || got.State != netaccess.StateConnected || got.Origin != "https://proxy-1.tail1234.ts.net" || got.Hostname != "proxy-1.tail1234.ts.net" || !got.UpdatedAt.Equal(at) {
		t.Fatalf("stored report = %#v", reloaded.NetworkAccess)
	}
	if url := reloaded.ClientURLFor(netaccess.Path{Provider: "tailscale"}); url != "https://proxy-1.tail1234.ts.net" {
		t.Fatalf("ClientURLFor from a stored row = %q", url)
	}

	// The list read shares the scan path; make sure it decodes too.
	listed, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, n := range listed {
		if n.ID == node.ID {
			found = true
			if _, ok := n.NetworkAccess["tailscale"]; !ok {
				t.Fatalf("listed row lost the report: %#v", n.NetworkAccess)
			}
		}
	}
	if !found {
		t.Fatal("created node missing from list")
	}

	// A check with no report clears the column back to its empty form.
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 0, 0, nil, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	reloaded, err = repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload after clear: %v", err)
	}
	if reloaded.NetworkAccess != nil {
		t.Fatalf("report survived a report-less health write: %#v", reloaded.NetworkAccess)
	}
	var raw string
	if err := repo.pool.QueryRow(ctx, `SELECT network_access::text FROM stream_nodes WHERE id = $1`, node.ID).Scan(&raw); err != nil {
		t.Fatalf("read column: %v", err)
	}
	if raw != "{}" {
		t.Fatalf("cleared column = %s, want {} (NOT NULL column)", raw)
	}
}

// Repointing the row at another machine drops the report with the rest of
// the identity-bound state: the overlay origin described the old worker.
func TestRepositoryUpdateURLClearsNetworkAccess(t *testing.T) {
	repo := newNetworkAccessTestRepo(t)
	ctx := context.Background()
	unique := time.Now().UnixNano()
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("network-access-move-%d", unique),
		Type: NodeTypeProxy,
		URL:  fmt.Sprintf("http://network-access-move-%d", unique),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	report := netaccess.NodeNetworkAccess{"tailscale": {State: netaccess.StateConnected, Origin: "https://old.ts.net"}}
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 0, 0, nil, report); err != nil {
		t.Fatalf("update health: %v", err)
	}
	sameName := node.Name
	if kept, err := repo.Update(ctx, node.ID, UpdateNodeInput{Name: &sameName}); err != nil || kept.NetworkAccess == nil {
		t.Fatalf("a non-URL edit dropped the report: %#v err=%v", kept, err)
	}
	moved := fmt.Sprintf("http://network-access-moved-%d", unique)
	updated, err := repo.Update(ctx, node.ID, UpdateNodeInput{URL: &moved})
	if err != nil {
		t.Fatalf("update url: %v", err)
	}
	if updated.NetworkAccess != nil {
		t.Fatalf("report survived a URL change: %#v", updated.NetworkAccess)
	}
}

func newNetworkAccessTestRepo(t *testing.T) *Repository {
	t.Helper()
	pool := newNodeTestPool(t)
	var column *string
	if err := pool.QueryRow(context.Background(),
		`SELECT column_name FROM information_schema.columns
		 WHERE table_name = 'stream_nodes' AND column_name = 'network_access'`).Scan(&column); err != nil {
		t.Skip("test database has not applied the node network_access migration")
	}
	return NewRepository(pool)
}

// The jsonb round trip must keep the wire keys the proxy sends, so a report a
// proxy produced with the netaccess types decodes identically after storage.
func TestNodeNetworkAccessJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	in := netaccess.NodeNetworkAccess{"tailscale": {State: netaccess.StateConnected, Origin: "https://p.ts.net", Hostname: "p.ts.net", UpdatedAt: at}}
	encoded, err := marshalNetworkAccess(in)
	if err != nil {
		t.Fatal(err)
	}
	var out netaccess.NodeNetworkAccess
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	if out["tailscale"] != in["tailscale"] {
		t.Fatalf("round trip = %#v, want %#v", out, in)
	}
	if empty, _ := marshalNetworkAccess(nil); string(empty) != "{}" {
		t.Fatalf("nil report marshals to %s, want {}", empty)
	}
}

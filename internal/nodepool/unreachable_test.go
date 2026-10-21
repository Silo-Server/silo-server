package nodepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// closedNodeURL returns the address of a port that was listening a moment ago
// and no longer is, so connecting to it is refused.
func closedNodeURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return "http://" + address
}

func refusedConnectError(t *testing.T) error {
	t.Helper()
	resp, err := http.Get(closedNodeURL(t) + "/api/v1/health")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to a closed port succeeded")
	}
	return err
}

func TestIsConnectFailure(t *testing.T) {
	timeout := &net.OpError{Op: dialOp, Net: "tcp", Err: os.ErrDeadlineExceeded}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "refused", err: refusedConnectError(t), want: true},
		{name: "dial timeout", err: &url.Error{Op: "Post", URL: "http://node", Err: timeout}, want: true},
		{name: "no route", err: &net.OpError{Op: dialOp, Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}, want: true},
		{name: "name does not resolve", err: &net.OpError{Op: dialOp, Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "node", IsNotFound: true}}, want: true},
		{name: "nil", err: nil, want: false},
		{name: "caller gave up", err: &url.Error{Op: "Get", URL: "http://node", Err: &net.OpError{Op: dialOp, Net: "tcp", Err: context.Canceled}}, want: false},
		{name: "local descriptors exhausted", err: &net.OpError{Op: dialOp, Net: "tcp", Err: os.NewSyscallError("socket", syscall.EMFILE)}, want: false},
		{name: "local ports exhausted", err: &net.OpError{Op: dialOp, Net: "tcp", Err: os.NewSyscallError("connect", syscall.EADDRNOTAVAIL)}, want: false},
		{name: "reset after connecting", err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, want: false},
		{name: "node stopped acknowledging", err: &url.Error{Op: "Post", URL: "http://node", Err: &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ETIMEDOUT)}}, want: true},
		{name: "caller deadline after connecting", err: &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, want: false},
		{name: "plain error", err: errors.New("node returned 503"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsConnectFailure(tt.err); got != tt.want {
				t.Fatalf("IsConnectFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestPlannerSkipsTranscodeNodeMarkedUnreachable(t *testing.T) {
	dead := &Node{ID: 1, URL: "http://dead:8080", Type: NodeTypeTranscode, Enabled: true, Healthy: true}
	live := &Node{ID: 2, URL: "http://live:8080", Type: NodeTypeTranscode, Enabled: true, Healthy: true, ActiveJobs: 3}
	transcodes := NewTranscodePool()
	transcodes.SetNodes([]*Node{dead, live})
	planner := NewPlanner(NewProxyPool(), transcodes)

	if got := planner.PlanSession("before", "", true, 0).TranscodeNode; got == nil || got.URL != dead.URL {
		t.Fatalf("before the mark the least-loaded node should win, got %+v", got)
	}
	if !ReportConnectFailure(planner, dead.URL+"/", refusedConnectError(t)) {
		t.Fatal("a refused connect did not mark a healthy node")
	}
	if planner.TranscodeNodeHealthy(dead.URL) {
		t.Fatal("marked node still reported healthy")
	}
	if got := planner.PlanSession("after", "", true, 0).TranscodeNode; got == nil || got.URL != live.URL {
		t.Fatalf("planner chose %+v, want the live node", got)
	}
	if ReportConnectFailure(planner, live.URL, errors.New("node returned 503")) {
		t.Fatal("an HTTP-level failure marked a node")
	}
	unresolved := &net.OpError{Op: dialOp, Net: "tcp", Err: &net.DNSError{Err: "i/o timeout", Name: "live", IsTimeout: true}}
	if ReportConnectFailure(planner, live.URL, unresolved) || !planner.TranscodeNodeHealthy(live.URL) {
		t.Fatal("a failed name lookup marked a node")
	}
}

// flakyHealthNode answers /health with 503 until it has refused failures
// requests, then with 200.
func flakyHealthNode(t *testing.T, failures int32) (string, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) <= failures {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok", ActiveJobs: 1})
	}))
	t.Cleanup(server.Close)
	return server.URL, &calls
}

type unreachableFixture struct {
	checker    *HealthChecker
	planner    *Planner
	pool       *TranscodePool
	broadcasts atomic.Int32
	announced  atomic.Value // last broadcast payload
}

func peerAnnouncement(t *testing.T, nodeURL string) string {
	t.Helper()
	payload, err := json.Marshal(unreachableAnnouncement{Origin: "another-api-server", URL: nodeURL})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func newUnreachableFixture(t *testing.T, nodeURL string) *unreachableFixture {
	t.Helper()
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 7, Name: "worker", URL: nodeURL, Type: NodeTypeTranscode, Enabled: true, Healthy: true}})
	f := &unreachableFixture{pool: pool, planner: NewPlanner(NewProxyPool(), pool)}
	f.checker = NewHealthChecker(NewProxyPool(), pool, nil)
	f.checker.recheckInterval = 10 * time.Millisecond
	f.checker.SetUnreachableBroadcast(func(payload string) {
		f.broadcasts.Add(1)
		f.announced.Store(payload)
	})
	f.planner.SetUnreachableHandler(f.checker.ReportUnreachable)
	return f
}

func TestUnreachableNodeThatAnswersARecheckReturnsAtOnce(t *testing.T) {
	nodeURL, calls := flakyHealthNode(t, 0)
	f := newUnreachableFixture(t, nodeURL)

	if !f.planner.MarkTranscodeNodeUnreachable(nodeURL) {
		t.Fatal("mark did not flip a healthy node")
	}
	f.checker.rechecks.Wait()

	if !f.planner.TranscodeNodeHealthy(nodeURL) {
		t.Fatal("a node that answered its recheck stayed out of selection")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("recheck sent %d health requests, want 1", got)
	}
	if got := f.broadcasts.Load(); got != 1 {
		t.Fatalf("local report broadcast %d times, want 1", got)
	}
}

func TestUnreachableNodeStaysOutUntilAHealthCheckSucceeds(t *testing.T) {
	nodeURL, calls := flakyHealthNode(t, unreachableRecheckAttempts)
	f := newUnreachableFixture(t, nodeURL)

	f.planner.MarkTranscodeNodeUnreachable(nodeURL)
	// A second report while the node is still out must not start another
	// recheck or announce it again.
	if f.planner.MarkTranscodeNodeUnreachable(nodeURL) {
		t.Fatal("an already unhealthy node was marked again")
	}
	f.checker.rechecks.Wait()

	if f.planner.TranscodeNodeHealthy(nodeURL) {
		t.Fatal("node came back without answering a health check")
	}
	if got := calls.Load(); got != unreachableRecheckAttempts {
		t.Fatalf("recheck sent %d health requests, want %d", got, unreachableRecheckAttempts)
	}
	if got := f.broadcasts.Load(); got != 1 {
		t.Fatalf("broadcast %d times, want 1", got)
	}

	f.checker.checkAll(context.Background())
	if !f.planner.TranscodeNodeHealthy(nodeURL) {
		t.Fatal("the next sweep did not restore a node that answered it")
	}
}

func TestPeerUnreachableReportIsRecheckedAndNotRebroadcast(t *testing.T) {
	nodeURL, _ := flakyHealthNode(t, 0)
	f := newUnreachableFixture(t, nodeURL)

	f.checker.ApplyUnreachableAnnouncement(peerAnnouncement(t, nodeURL+"/"))
	f.checker.rechecks.Wait()

	if !f.planner.TranscodeNodeHealthy(nodeURL) {
		t.Fatal("a node this process can reach stayed out after a peer's report")
	}
	if got := f.broadcasts.Load(); got != 0 {
		t.Fatalf("peer report was rebroadcast %d times", got)
	}
}

func TestRecheckStopsWithTheCheckerContext(t *testing.T) {
	nodeURL := closedNodeURL(t)
	f := newUnreachableFixture(t, nodeURL)
	f.checker.recheckInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	f.checker.mu.Lock()
	f.checker.ctx = ctx //nolint:fatcontext // installs the test's ctx once, as Start would
	f.checker.mu.Unlock()

	f.planner.MarkTranscodeNodeUnreachable(nodeURL)
	cancel()
	waitForRechecks(t, f.checker)
}

func TestRecheckStopsWhenTheNodeLeavesThePool(t *testing.T) {
	nodeURL := closedNodeURL(t)
	f := newUnreachableFixture(t, nodeURL)
	f.checker.recheckInterval = 50 * time.Millisecond

	f.planner.MarkTranscodeNodeUnreachable(nodeURL)
	f.pool.SetNodes(nil)
	waitForRechecks(t, f.checker)
}

func waitForRechecks(t *testing.T, checker *HealthChecker) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		checker.rechecks.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("recheck did not stop")
	}
}

func TestOwnAnnouncementEchoDoesNotUndoARecheck(t *testing.T) {
	nodeURL, calls := flakyHealthNode(t, 0)
	f := newUnreachableFixture(t, nodeURL)

	f.planner.MarkTranscodeNodeUnreachable(nodeURL)
	f.checker.rechecks.Wait()
	own, _ := f.announced.Load().(string)
	f.checker.ApplyUnreachableAnnouncement(own)
	f.checker.rechecks.Wait()

	if !f.planner.TranscodeNodeHealthy(nodeURL) {
		t.Fatal("the echo of this server's own announcement pulled a rechecked node out")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("health requests = %d, want only the first recheck", got)
	}

	// A peer's report after that recovery is new evidence, not the echo.
	f.checker.ApplyUnreachableAnnouncement(peerAnnouncement(t, nodeURL))
	f.checker.rechecks.Wait()
	if got := calls.Load(); got != 2 {
		t.Fatalf("health requests = %d, want a recheck for the peer's report", got)
	}
}

func TestNodeMarkedAgainAfterARecheckIsRecheckedAgain(t *testing.T) {
	nodeURL, calls := flakyHealthNode(t, 0)
	f := newUnreachableFixture(t, nodeURL)

	for range 2 {
		if !f.planner.MarkTranscodeNodeUnreachable(nodeURL) {
			t.Fatal("mark did not flip a healthy node")
		}
		f.checker.rechecks.Wait()
		if !f.planner.TranscodeNodeHealthy(nodeURL) {
			t.Fatal("node stayed out after answering its recheck")
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("health requests = %d, want one recheck per mark", got)
	}
}

func TestRecheckRefreshesCapabilitiesOfANodeThatCameBackChanged(t *testing.T) {
	nodeURL := newHealthNode(t, "hash-after-restart")
	f := newUnreachableFixture(t, nodeURL)
	fetcher := &fakeCapabilityFetcher{payload: []byte(`{"transformations":[]}`), hash: "hash-after-restart"}
	f.checker.SetCapabilityFetcher(fetcher.fetch)

	f.planner.MarkTranscodeNodeUnreachable(nodeURL)
	f.checker.rechecks.Wait()
	f.checker.waitForCapabilityRefreshes()

	if got := fetcher.callCount(); got != 1 {
		t.Fatalf("capability fetches = %d, want 1 after the node came back with a new hash", got)
	}
}

// The node configuration reconciliation replaces the pool with database rows
// every few seconds, and those carry the last sweep's verdict.
func TestUnreachableMarkSurvivesPoolReload(t *testing.T) {
	row := func() []*Node {
		return []*Node{{ID: 3, URL: "http://node:8080/", Type: NodeTypeTranscode, Enabled: true, Healthy: true}}
	}
	pool := NewTranscodePool()
	pool.SetNodes(row())
	planner := NewPlanner(NewProxyPool(), pool)

	if !pool.MarkUnreachable("http://node:8080") {
		t.Fatal("mark did not flip a healthy node")
	}
	pool.SetNodes(row())
	if planner.TranscodeNodeHealthy("http://node:8080") {
		t.Fatal("a pool reload from stale rows undid the mark")
	}

	pool.ApplyHealthReport(3, "http://node:8080", HealthReport{Healthy: true}, time.Now())
	pool.SetNodes(row())
	if !planner.TranscodeNodeHealthy("http://node:8080") {
		t.Fatal("a successful health check did not clear the mark")
	}

	pool.MarkUnreachable("http://node:8080")
	pool.SetNodes(nil)
	pool.SetNodes(row())
	if !planner.TranscodeNodeHealthy("http://node:8080") {
		t.Fatal("the mark outlived the node leaving the pool")
	}
}

// A recheck that finds the node answering is persisted, like a sweep result:
// the node configuration reconciliation rebuilds the pool from the stored row,
// and a sweep that saw the node down would otherwise take it back out.
func TestRecheckPersistsARecoveredNode(t *testing.T) {
	db := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(db)
	nodeURL, _ := flakyHealthNode(t, 0)
	node, err := repo.Create(ctx, CreateNodeInput{Name: fmt.Sprintf("recheck-test-%d", time.Now().UnixNano()), Type: NodeTypeTranscode, URL: nodeURL})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, false, 0, 0, nil, nil); err != nil {
		t.Fatalf("record failed sweep: %v", err)
	}

	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: node.ID, URL: node.URL, Type: NodeTypeTranscode, Enabled: true, Healthy: true}})
	checker := NewHealthChecker(NewProxyPool(), pool, repo)
	checker.ReportUnreachable(nodeURL)
	checker.rechecks.Wait()

	stored, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if !stored.Healthy {
		t.Fatal("the recheck's healthy result was not persisted")
	}
}

func TestMarkUnreachableCoversEveryEntryForTheEndpoint(t *testing.T) {
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{
		{ID: 1, URL: "http://worker:8080", Type: NodeTypeTranscode, Enabled: true, Healthy: true},
		{ID: 2, URL: "http://worker:8080/", Type: NodeTypeTranscode, Enabled: true, Healthy: true},
	})
	if !pool.MarkUnreachable("http://worker:8080") {
		t.Fatal("mark did not flip a healthy node")
	}
	for _, n := range pool.Nodes() {
		if n.Healthy {
			t.Fatalf("node %d at the same endpoint is still healthy", n.ID)
		}
	}
}

func TestHealthResultForAnotherNodeDoesNotClearTheMark(t *testing.T) {
	row := func() []*Node {
		return []*Node{{ID: 4, URL: "http://worker:8080", Type: NodeTypeTranscode, Enabled: true, Healthy: true}}
	}
	pool := NewTranscodePool()
	pool.SetNodes(row())
	pool.MarkUnreachable("http://worker:8080")

	// A late result for a node that used to have this URL under another id.
	pool.ApplyHealthReport(3, "http://worker:8080", HealthReport{Healthy: true}, time.Now())
	pool.SetNodes(row())
	if pool.FindByURL("http://worker:8080").Healthy {
		t.Fatal("a result about another node cleared the mark")
	}
}

func TestRecheckRestoresEveryEntryForTheEndpoint(t *testing.T) {
	nodeURL, _ := flakyHealthNode(t, 0)
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{
		{ID: 1, URL: nodeURL, Type: NodeTypeTranscode, Enabled: true, Healthy: true},
		{ID: 2, URL: nodeURL + "/", Type: NodeTypeTranscode, Enabled: true, Healthy: true},
	})
	checker := NewHealthChecker(NewProxyPool(), pool, nil)
	checker.ReportUnreachable(nodeURL)
	checker.rechecks.Wait()

	for _, n := range pool.Nodes() {
		if !n.Healthy {
			t.Fatalf("node %d at the rechecked endpoint is still unhealthy", n.ID)
		}
	}
}

// A node can come back from a connection failure draining, after a restart
// that is winding it down. The recheck must publish that, or placement would
// send it new work until the next sweep.
func TestRecheckPublishesTheDrainStateTheNodeAnsweredWith(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(healthResponse{Status: string(DrainStateDraining)})
	}))
	t.Cleanup(server.Close)
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 1, URL: server.URL, Type: NodeTypeTranscode, Enabled: true, Healthy: true}})
	checker := NewHealthChecker(NewProxyPool(), pool, nil)
	checker.ReportUnreachable(server.URL)
	checker.rechecks.Wait()

	n := pool.FindByURL(server.URL)
	if !n.Healthy || n.DrainState != DrainStateDraining {
		t.Fatalf("after the recheck healthy=%v drain_state=%q, want healthy and draining", n.Healthy, n.DrainState)
	}
	if n.TakesNewWork() {
		t.Fatal("a node that answered draining takes new work")
	}
}

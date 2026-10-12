package nodepool

import (
	"context"
	"encoding/json"
	"errors"
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
}

func newUnreachableFixture(t *testing.T, nodeURL string) *unreachableFixture {
	t.Helper()
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 7, Name: "worker", URL: nodeURL, Type: NodeTypeTranscode, Enabled: true, Healthy: true}})
	f := &unreachableFixture{pool: pool, planner: NewPlanner(NewProxyPool(), pool)}
	f.checker = NewHealthChecker(NewProxyPool(), pool, nil)
	f.checker.recheckInterval = 10 * time.Millisecond
	f.checker.SetUnreachableBroadcast(func(string) { f.broadcasts.Add(1) })
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

	f.checker.ApplyPeerUnreachable(nodeURL + "/")
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
	f.checker.ctx = ctx
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
	f.checker.ApplyPeerUnreachable(nodeURL)
	f.checker.rechecks.Wait()

	if !f.planner.TranscodeNodeHealthy(nodeURL) {
		t.Fatal("the echo of this server's own announcement pulled a rechecked node out")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("health requests = %d, want only the first recheck", got)
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

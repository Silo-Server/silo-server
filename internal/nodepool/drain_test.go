package nodepool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func drainingNode(id int, url string, activeJobs int) *Node {
	n := transcodeNode(id, url, nil, activeJobs)
	n.DrainRequested = true
	return n
}

// A drained node's sessions finish while new ones go elsewhere, including when
// the drained node is the least loaded.
func TestDrainingNodeTakesNoNewSession(t *testing.T) {
	f := newFixture([]*Node{proxyNode(1, "http://proxy", nil)}, []*Node{
		drainingNode(2, "http://tc-draining", 0),
		transcodeNode(3, "http://tc-busy", nil, 5),
	})

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-busy" {
		t.Fatalf("new session placed on %+v, want tc-busy", plan.TranscodeNode)
	}
	local := f.planner.PlanTranscodeSessionWithLocalEgress("s2", "", nil)
	if local.TranscodeNode == nil || local.TranscodeNode.URL != "http://tc-busy" {
		t.Fatalf("local-egress session placed on %+v, want tc-busy", local.TranscodeNode)
	}
}

// A node that reports draining on its own (SIGTERM) is excluded the same way
// as one an administrator drained.
func TestNodeReportingDrainTakesNoNewSession(t *testing.T) {
	reporting := transcodeNode(2, "http://tc-stopping", nil, 0)
	reporting.DrainState = DrainStateDraining
	f := newFixture([]*Node{proxyNode(1, "http://proxy", nil)}, []*Node{reporting})

	if plan := f.planner.PlanSession("s1", "", true, 0); plan.TranscodeNode != nil {
		t.Fatalf("new session placed on a node reporting draining: %+v", plan.TranscodeNode)
	}
}

// Planning a session again (a Jellyfin client re-requesting its master
// playlist) must keep it on the draining node that runs it; with no other node
// available, excluding it would tear the stream down.
func TestDrainingNodeKeepsItsCurrentSession(t *testing.T) {
	f := newFixture([]*Node{proxyNode(1, "http://proxy", nil)}, []*Node{drainingNode(2, "http://tc-draining/", 1)})

	plan := f.planner.PlanSession("s1", "http://tc-draining/", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-draining" {
		t.Fatalf("current session planned onto %+v, want its draining node", plan.TranscodeNode)
	}
	if other := f.planner.PlanSession("s2", "http://tc-elsewhere", true, 0); other.TranscodeNode != nil {
		t.Fatalf("a session from another node was placed on the draining node: %+v", other.TranscodeNode)
	}
}

func TestDrainingNodeTakesNoReservedWork(t *testing.T) {
	f := newFixture(nil, []*Node{drainingNode(1, "http://tc-draining", 0)})

	if node, release := f.planner.ReserveTranscodeWork("download"); node != nil {
		release()
		t.Fatalf("reserved work on a draining node: %+v", node)
	}
	if f.planner.TranscodeWorkAvailableWith(nil) {
		t.Fatal("a draining node reported as available for work")
	}

	f.transcodes.SetNodes([]*Node{drainingNode(1, "http://tc-draining", 0), transcodeNode(2, "http://tc-open", nil, 3)})
	node, release := f.planner.ReserveTranscodeWork("download")
	defer release()
	if node == nil || node.URL != "http://tc-open" {
		t.Fatalf("reserved %+v, want tc-open", node)
	}
}

// Side work reserved per feature reads a cluster whose only node drains as
// busy, not as having no node: the drain is temporary.
func TestReservationsSkipDrainingNodes(t *testing.T) {
	var reservations Reservations
	node, release, outcome := reservations.Reserve([]*Node{drainingNode(1, "http://tc-draining", 0)}, 2)
	release()
	if node != nil || outcome != CapacityExhausted {
		t.Fatalf("Reserve = %+v, %v; want no node, CapacityExhausted", node, outcome)
	}
	node, release, outcome = reservations.Reserve([]*Node{drainingNode(1, "http://tc-draining", 0), transcodeNode(2, "http://tc-open", nil, 4)}, 2)
	defer release()
	if node == nil || node.URL != "http://tc-open" || outcome != Reserved {
		t.Fatalf("Reserve = %+v, %v; want tc-open", node, outcome)
	}
}

// The status strings are the wire contract with the node's health handler;
// transcodenode's tests pin its constants to DrainState.
func TestCheckNodeReportReadsDrainStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   DrainState
	}{
		{"ok", DrainStateNone},
		{"draining", DrainStateDraining},
		{"drained", DrainStateDrained},
		{"something-newer", DrainStateNone},
	} {
		url := newStatsHealthNode(t, `{"status":"`+tc.status+`","active_jobs":1}`)
		report := CheckNodeReport(context.Background(), &Node{URL: url})
		if !report.Healthy || report.ActiveJobs != 1 || report.DrainState != tc.want {
			t.Fatalf("status %q: report = %+v, want healthy with drain state %q", tc.status, report, tc.want)
		}
	}
	if report := CheckNodeReport(context.Background(), &Node{URL: "http://127.0.0.1:1"}); report.Healthy || report.DrainState != DrainStateNone {
		t.Fatalf("unreachable node: report = %+v", report)
	}
}

// The sweep publishes the node's drain state to the pool; a later check that
// finds it no longer draining clears it.
func TestHealthSweepAppliesDrainState(t *testing.T) {
	status := "draining"
	url := newStatsHealthNodeFunc(t, func() string { return `{"status":"` + status + `","active_jobs":2}` })
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 7, URL: url, Type: NodeTypeTranscode, Enabled: true}})
	checker := NewHealthChecker(NewProxyPool(), pool, nil)

	checker.checkAll(context.Background())
	if got := pool.Nodes()[0]; !got.Healthy || got.DrainState != DrainStateDraining || !got.Draining() {
		t.Fatalf("after a draining check: %+v", got)
	}

	status = "ok"
	if !checker.CheckTranscodeNode(context.Background(), 7) {
		t.Fatal("CheckTranscodeNode did not find the pooled node")
	}
	if got := pool.Nodes()[0]; got.DrainState != DrainStateNone || got.Draining() {
		t.Fatalf("after an ok check: %+v", got)
	}
	if checker.CheckTranscodeNode(context.Background(), 8) {
		t.Fatal("CheckTranscodeNode reported an unknown node")
	}
}

// Checks of one node run one at a time, so the answer written last is the one
// asked for last: a sweep answer from before the node began draining must not
// overwrite the recheck its announcement caused.
func TestHealthChecksOfOneNodeRunInOrder(t *testing.T) {
	var requests atomic.Int32
	firstArrived, releaseFirst := make(chan struct{}), make(chan struct{})
	url := newStatsHealthNodeFunc(t, func() string {
		if requests.Add(1) == 1 {
			close(firstArrived)
			<-releaseFirst
			return `{"status":"ok"}`
		}
		return `{"status":"draining"}`
	})
	pool := NewTranscodePool()
	pool.SetNodes([]*Node{{ID: 7, URL: url, Type: NodeTypeTranscode, Enabled: true}})
	checker := NewHealthChecker(NewProxyPool(), pool, nil)

	sweepDone, recheckDone := make(chan struct{}), make(chan struct{})
	go func() { checker.checkAll(context.Background()); close(sweepDone) }()
	<-firstArrived
	go func() { checker.CheckTranscodeNode(context.Background(), 7); close(recheckDone) }()
	// Give an unserialized recheck the chance to answer first; a serialized one
	// waits for the sweep's check whatever the timing.
	select {
	case <-recheckDone:
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	<-sweepDone
	<-recheckDone
	if got := pool.Nodes()[0]; requests.Load() != 2 || got.DrainState != DrainStateDraining {
		t.Fatalf("after a sweep answer and a later recheck: %d requests, drain state %q", requests.Load(), got.DrainState)
	}
}

// ApplyHealth, the positional writer other callers still use, must not touch a
// drain state it was not given.
func TestApplyHealthKeepsDrainState(t *testing.T) {
	pool := NewTranscodePool()
	node := transcodeNode(1, "http://tc", nil, 0)
	node.DrainState = DrainStateDrained
	pool.SetNodes([]*Node{node})

	pool.ApplyHealth(1, "http://tc", true, 0, 0, "", nil, nil, node.CreatedAt)
	if got := pool.Nodes()[0].DrainState; got != DrainStateDrained {
		t.Fatalf("drain state = %q after ApplyHealth, want it kept", got)
	}
}

// newStatsHealthNodeFunc is newStatsHealthNode with a body read per request,
// so a test can change what the node answers between checks.
func newStatsHealthNodeFunc(t *testing.T, body func() string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body()))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestTakesNewWork(t *testing.T) {
	for _, test := range []struct {
		name string
		node *Node
		want bool
	}{
		{"nil", nil, false},
		{"open", transcodeNode(1, "http://tc", nil, 0), true},
		{"disabled", &Node{Healthy: true}, false},
		{"unhealthy", &Node{Enabled: true}, false},
		{"drain requested", drainingNode(1, "http://tc", 0), false},
		{"reports drained", &Node{Enabled: true, Healthy: true, DrainState: DrainStateDrained}, false},
	} {
		if got := test.node.TakesNewWork(); got != test.want {
			t.Errorf("%s: TakesNewWork() = %v, want %v", test.name, got, test.want)
		}
	}
}

// A drained node runs no session, so even a session it used to run is planned
// elsewhere; the new-work node list and the drained lookup agree.
func TestDrainedNodeLosesCurrentSessionAffinity(t *testing.T) {
	drained := drainingNode(2, "http://tc-drained", 0)
	drained.DrainState = DrainStateDrained
	f := newFixture([]*Node{proxyNode(1, "http://proxy", nil)}, []*Node{drained, transcodeNode(3, "http://tc-open", nil, 4)})

	plan := f.planner.PlanSession("s1", "http://tc-drained", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-open" {
		t.Fatalf("session from a drained node planned onto %+v, want tc-open", plan.TranscodeNode)
	}
	if !f.planner.TranscodeNodeDrained("http://tc-drained/") || f.planner.TranscodeNodeDrained("http://tc-open") {
		t.Fatal("TranscodeNodeDrained disagrees with the pooled drain states")
	}
	if got := f.planner.TranscodeNodeURLsForNewWork(); len(got) != 1 || got[0] != "http://tc-open" {
		t.Fatalf("TranscodeNodeURLsForNewWork = %v, want only tc-open", got)
	}
	if got := f.planner.TranscodeNodeURLs(); len(got) != 2 {
		t.Fatalf("TranscodeNodeURLs = %v, want both pooled nodes", got)
	}
}

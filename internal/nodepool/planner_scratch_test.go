package nodepool

import (
	"testing"
)

// A transcode writes segments to its scratch volume for the life of the session,
// so admitting one onto a nearly full node buys a stream that dies mid-playback.
// With a healthy sibling available the pressured node must lose, even though it
// carries fewer jobs and would otherwise win outright.
func TestPlanSessionSkipsScratchPressuredTranscodeNode(t *testing.T) {
	full := transcodeNode(1, "http://tc-full", nil, 0)
	full.LastStats = scratchStats(96, 100)
	roomy := transcodeNode(2, "http://tc-roomy", nil, 3)
	roomy.LastStats = scratchStats(20, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{full, roomy})

	plan := f.planner.PlanSession("session-1", "", true, 0)
	if plan.TranscodeNode == nil {
		t.Fatal("no transcode node selected")
	}
	if plan.TranscodeNode.URL != "http://tc-roomy" {
		t.Fatalf("selected %s, want the node with scratch headroom", plan.TranscodeNode.URL)
	}
}

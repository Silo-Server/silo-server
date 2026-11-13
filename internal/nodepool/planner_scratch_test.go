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

// The guard is soft on purpose. If every otherwise-eligible node is over the
// threshold, refusing to plan would turn a degraded cluster into a dark one, and
// the threshold was never chosen to be a kill switch.
func TestPlanSessionIgnoresScratchGuardWhenEveryNodeIsPressured(t *testing.T) {
	first := transcodeNode(1, "http://tc-a", nil, 5)
	first.LastStats = scratchStats(99, 100)
	second := transcodeNode(2, "http://tc-b", nil, 1)
	second.LastStats = scratchStats(97, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{first, second})

	plan := f.planner.PlanSession("session-1", "", true, 0)
	if plan.TranscodeNode == nil {
		t.Fatal("scratch pressure emptied the pool; degraded service must beat no service")
	}
	// With the guard dropped the ordinary least-jobs rule decides.
	if plan.TranscodeNode.URL != "http://tc-b" {
		t.Fatalf("selected %s, want the least-loaded node once the guard is ignored", plan.TranscodeNode.URL)
	}
	if plan.ProxyNode == nil {
		t.Fatal("no proxy paired with the fallback transcode node")
	}
}

// A node whose sample cannot be read must not be excluded: the guard fires on
// measured pressure only, and a node predating the scratch flag reports none.
func TestPlanSessionAdmitsNodeWithoutScratchStats(t *testing.T) {
	unknown := transcodeNode(1, "http://tc-unknown", nil, 0)
	pressured := transcodeNode(2, "http://tc-full", nil, 0)
	pressured.LastStats = scratchStats(99, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{unknown, pressured})

	plan := f.planner.PlanSession("session-1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-unknown" {
		t.Fatalf("selected %v, want the node with no scratch reading", plan.TranscodeNode)
	}
}

// Soft affinity keeps a session on its current node unless a candidate is two
// jobs better. Scratch pressure has to beat that: staying is what kills the
// stream, and the affinity rule exists to avoid gratuitous switches, not to
// defend a full disk.
func TestPlanSessionMovesOffAScratchPressuredCurrentNode(t *testing.T) {
	current := transcodeNode(1, "http://tc-current", nil, 0)
	current.LastStats = scratchStats(99, 100)
	other := transcodeNode(2, "http://tc-other", nil, 0)
	other.LastStats = scratchStats(10, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{current, other})

	plan := f.planner.PlanSession("session-1", "http://tc-current", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-other" {
		t.Fatalf("selected %v, want the session moved off the full-scratch node", plan.TranscodeNode)
	}
}

// The local-egress path admits transcodes without a proxy partner and must apply
// the same guard, since it is the same scratch volume being written.
func TestPlanTranscodeSessionWithLocalEgressAppliesScratchGuard(t *testing.T) {
	full := transcodeNode(1, "http://tc-full", nil, 0)
	full.LastStats = scratchStats(96, 100)
	roomy := transcodeNode(2, "http://tc-roomy", nil, 2)
	roomy.LastStats = scratchStats(20, 100)
	f := newFixture(nil, []*Node{full, roomy})

	plan := f.planner.PlanTranscodeSessionWithLocalEgress("session-1", "", nil)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-roomy" {
		t.Fatalf("selected %v, want the node with scratch headroom", plan.TranscodeNode)
	}
}

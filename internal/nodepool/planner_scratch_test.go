package nodepool

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// captureScratchLogs redirects the default logger, which is what selection logs
// against: PlanSession takes no context, so there is no handler to inject.
func captureScratchLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}

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

// The WARN is the operator's only signal about scratch pressure, so it must not
// claim an exclusion that did not happen. When every eligible node is over the
// threshold the guard gives way and the pressured node takes the session — the
// opposite of "excluded from selection", and a far more urgent state.
func TestScratchPressureWarningReportsTheDegradedAdmission(t *testing.T) {
	logs := captureScratchLogs(t)
	first := transcodeNode(1, "http://tc-a", nil, 5)
	first.LastStats = scratchStats(99, 100)
	second := transcodeNode(2, "http://tc-b", nil, 1)
	second.LastStats = scratchStats(97, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{first, second})

	plan := f.planner.PlanSession("session-1", "", true, 0)
	if plan.TranscodeNode == nil {
		t.Fatal("scratch pressure emptied the pool; degraded service must beat no service")
	}

	output := logs.String()
	if strings.Contains(output, "excluded from selection") {
		t.Fatalf("logged an exclusion for a node it then selected (%s):\n%s", plan.TranscodeNode.URL, output)
	}
	if !strings.Contains(output, "still selected because no eligible node has scratch headroom") {
		t.Fatalf("no degraded-admission warning:\n%s", output)
	}
	if !strings.Contains(output, "transcode scratch guard ignored") {
		t.Fatalf("the cluster-wide guard-dropped state was never reported:\n%s", output)
	}
}

// With a sibling that has headroom the guard really does exclude, and the
// message has to say so — the two states are told apart by wording, so both have
// to be exercised.
func TestScratchPressureWarningReportsARealExclusion(t *testing.T) {
	logs := captureScratchLogs(t)
	full := transcodeNode(1, "http://tc-full", nil, 0)
	full.LastStats = scratchStats(99, 100)
	roomy := transcodeNode(2, "http://tc-roomy", nil, 3)
	roomy.LastStats = scratchStats(20, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{full, roomy})

	f.planner.PlanSession("session-1", "", true, 0)

	output := logs.String()
	if !strings.Contains(output, "excluded from selection") {
		t.Fatalf("no exclusion warning while a node with headroom existed:\n%s", output)
	}
	if strings.Contains(output, "transcode scratch guard ignored") {
		t.Fatalf("reported the guard as dropped while it was honored:\n%s", output)
	}
}

// The guard-dropped state is latched like the per-node pressure warnings, and
// reports its way back out: an operator who saw the outage warning needs to know
// when steering resumed.
func TestScratchGuardDroppedStateLatchesAndRecovers(t *testing.T) {
	logs := captureScratchLogs(t)
	only := transcodeNode(1, "http://tc-a", nil, 0)
	only.LastStats = scratchStats(99, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{only})

	for range 3 {
		f.planner.PlanSession("session-1", "", true, 0)
	}
	if got := strings.Count(logs.String(), "transcode scratch guard ignored"); got != 1 {
		t.Fatalf("guard-dropped warnings = %d across three plans, want exactly one", got)
	}

	recovered := transcodeNode(1, "http://tc-a", nil, 0)
	recovered.LastStats = scratchStats(10, 100)
	f.transcodes.SetNodes([]*Node{recovered})
	f.planner.PlanSession("session-2", "", true, 0)

	if !strings.Contains(logs.String(), "transcode scratch guard back in force") {
		t.Fatalf("the guard coming back into force was never reported:\n%s", logs.String())
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

// The warning is latched so a full disk does not produce a log line per session
// start. The latch is internal state, so this asserts the transitions it records
// rather than the log output.
func TestScratchPressureWarningLatchesPerTransition(t *testing.T) {
	node := transcodeNode(1, "http://tc-a", nil, 0)
	node.LastStats = scratchStats(99, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{node})

	for range 3 {
		f.planner.PlanSession("session-1", "", true, 0)
	}
	f.planner.mu.Lock()
	latched := len(f.planner.scratchPressed)
	pressed := f.planner.scratchPressed[1]
	f.planner.mu.Unlock()
	if latched != 1 || !pressed {
		t.Fatalf("latch = %v, want node 1 latched once", f.planner.scratchPressed)
	}

	// Recovery clears the latch, so the next episode warns again.
	recovered := transcodeNode(1, "http://tc-a", nil, 0)
	recovered.LastStats = scratchStats(10, 100)
	f.transcodes.SetNodes([]*Node{recovered})
	f.planner.PlanSession("session-2", "", true, 0)

	f.planner.mu.Lock()
	latched = len(f.planner.scratchPressed)
	f.planner.mu.Unlock()
	if latched != 0 {
		t.Fatalf("latch = %v after recovery, want it cleared", f.planner.scratchPressed)
	}
}

// A node that leaves the pool must not leave a latch entry behind: the same id
// coming back still full would then never warn.
func TestScratchPressureLatchIsPrunedWhenANodeLeavesThePool(t *testing.T) {
	node := transcodeNode(1, "http://tc-a", nil, 0)
	node.LastStats = scratchStats(99, 100)
	f := newFixture([]*Node{proxyNode(10, "http://proxy", nil)}, []*Node{node})
	f.planner.PlanSession("session-1", "", true, 0)

	f.transcodes.SetNodes([]*Node{transcodeNode(2, "http://tc-b", nil, 0)})
	f.planner.PlanSession("session-2", "", true, 0)

	f.planner.mu.Lock()
	_, stillLatched := f.planner.scratchPressed[1]
	f.planner.mu.Unlock()
	if stillLatched {
		t.Fatal("latch entry survived the node leaving the pool")
	}
}

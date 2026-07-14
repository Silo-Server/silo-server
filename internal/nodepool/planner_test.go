package nodepool

import (
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

type plannerFixture struct {
	planner    *Planner
	proxies    *ProxyPool
	transcodes *TranscodePool
	now        time.Time
}

func newFixture(proxies, transcodes []*Node) *plannerFixture {
	pp := NewProxyPool()
	pp.SetNodes(proxies)
	tp := NewTranscodePool()
	tp.SetNodes(transcodes)
	f := &plannerFixture{
		planner:    NewPlanner(pp, tp),
		proxies:    pp,
		transcodes: tp,
		now:        time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	}
	f.planner.now = func() time.Time { return f.now }
	return f
}

func proxyNode(id int, url string, group *string) *Node {
	return &Node{ID: id, Name: url, Type: NodeTypeProxy, URL: url, Enabled: true, Healthy: true, Group: group}
}

func transcodeNode(id int, url string, group *string, activeJobs int) *Node {
	return &Node{ID: id, Name: url, Type: NodeTypeTranscode, URL: url, Enabled: true, Healthy: true, Group: group, ActiveJobs: activeJobs}
}

func TestPlanTranscodePairsProxyFromSameGroup(t *testing.T) {
	f := newFixture(
		[]*Node{
			proxyNode(1, "http://proxy-a", strPtr("rack-a")),
			proxyNode(2, "http://proxy-b", strPtr("rack-b")),
		},
		[]*Node{
			transcodeNode(3, "http://tc-a", strPtr("rack-a"), 5),
			transcodeNode(4, "http://tc-b", strPtr("rack-b"), 0),
		},
	)

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-b" {
		t.Fatalf("expected least-loaded tc-b, got %+v", plan.TranscodeNode)
	}
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-b" {
		t.Fatalf("expected same-group proxy-b, got %+v", plan.ProxyNode)
	}
}

func TestReleaseSessionDropsProvisionalReservation(t *testing.T) {
	node := transcodeNode(1, "http://tc-1", nil, 0)
	node.MaxJobs = intPtr(1)
	f := newFixture(nil, []*Node{node})
	if got := f.planner.PlanSession("s1", "", true, 0).TranscodeNode; got == nil {
		t.Fatal("first session was not reserved")
	}
	if got := f.planner.PlanSession("s2", "", true, 0).TranscodeNode; got != nil {
		t.Fatalf("second session bypassed reservation: %+v", got)
	}
	f.planner.ReleaseSession("s1")
	if got := f.planner.PlanSession("s2", "", true, 0).TranscodeNode; got == nil {
		t.Fatal("released reservation still blocked the node")
	}
}

func TestDegradedGroupExcludesItsTranscodeNodes(t *testing.T) {
	unhealthyProxy := proxyNode(1, "http://proxy-a", strPtr("rack-a"))
	unhealthyProxy.Healthy = false
	f := newFixture(
		[]*Node{
			unhealthyProxy,
			proxyNode(2, "http://proxy-b", strPtr("rack-b")),
		},
		[]*Node{
			transcodeNode(3, "http://tc-a", strPtr("rack-a"), 0), // idle but group degraded
			transcodeNode(4, "http://tc-b", strPtr("rack-b"), 9),
		},
	)

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-b" {
		t.Fatalf("expected tc-b (rack-a degraded), got %+v", plan.TranscodeNode)
	}
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-b" {
		t.Fatalf("expected proxy-b, got %+v", plan.ProxyNode)
	}
}

func TestUnhealthyTranscodeMemberDegradesGroup(t *testing.T) {
	deadTC := transcodeNode(5, "http://tc-a2", strPtr("rack-a"), 0)
	deadTC.Healthy = false
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-a", strPtr("rack-a"))},
		[]*Node{
			transcodeNode(3, "http://tc-a1", strPtr("rack-a"), 0),
			deadTC,
		},
	)

	// All enabled members of a group must be healthy for the group to be
	// eligible — even the healthy sibling is excluded.
	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode != nil {
		t.Fatalf("expected no transcode node, got %+v", plan.TranscodeNode)
	}
}

func TestUngroupedNodesKeepLegacyBehavior(t *testing.T) {
	f := newFixture(
		[]*Node{
			proxyNode(1, "http://proxy-1", nil),
			proxyNode(2, "http://proxy-2", nil),
		},
		[]*Node{
			transcodeNode(3, "http://tc-1", nil, 2),
			transcodeNode(4, "http://tc-2", nil, 1),
		},
	)

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-2" {
		t.Fatalf("expected least-connections tc-2, got %+v", plan.TranscodeNode)
	}
	if plan.ProxyNode == nil {
		t.Fatal("expected a proxy node")
	}

	// Round-robin across both proxies for subsequent sessions.
	first := plan.ProxyNode.URL
	second := f.planner.PlanSession("s2", "", true, 0).ProxyNode.URL
	if first == second {
		t.Fatalf("expected round-robin to alternate proxies, got %s twice", first)
	}
}

func TestGroupWithoutProxiesFallsBackToGlobalProxy(t *testing.T) {
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{transcodeNode(2, "http://tc-a", strPtr("rack-a"), 0)},
	)

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-a" {
		t.Fatalf("expected tc-a, got %+v", plan.TranscodeNode)
	}
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-1" {
		t.Fatalf("expected global proxy fallback, got %+v", plan.ProxyNode)
	}
}

func TestSoftAffinityKeepsCurrentNode(t *testing.T) {
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{
			transcodeNode(2, "http://tc-1", nil, 2),
			transcodeNode(3, "http://tc-2", nil, 1),
		},
	)

	// Difference of 1 job: stay on current.
	plan := f.planner.PlanSession("s1", "http://tc-1", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-1" {
		t.Fatalf("expected soft affinity to keep tc-1, got %+v", plan.TranscodeNode)
	}

	// Difference of 2+: switch to the less-loaded node.
	f.transcodes.Nodes()[0].ActiveJobs = 4
	plan = f.planner.PlanSession("s1", "http://tc-1", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-2" {
		t.Fatalf("expected switch to tc-2, got %+v", plan.TranscodeNode)
	}
}

func TestTranscodeCapSkipsFullNode(t *testing.T) {
	capped := transcodeNode(2, "http://tc-1", nil, 3)
	capped.MaxJobs = intPtr(3)
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{
			capped,
			transcodeNode(3, "http://tc-2", nil, 5),
		},
	)

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-2" {
		t.Fatalf("expected at-cap tc-1 to be skipped, got %+v", plan.TranscodeNode)
	}

	// All nodes at cap: no transcode node.
	f.transcodes.Nodes()[1].MaxJobs = intPtr(5)
	plan = f.planner.PlanSession("s2", "", true, 0)
	if plan.TranscodeNode != nil {
		t.Fatalf("expected no eligible node, got %+v", plan.TranscodeNode)
	}
}

func TestProxyCapSkipsFullProxy(t *testing.T) {
	capped := proxyNode(1, "http://proxy-1", nil)
	capped.MaxJobs = intPtr(2)
	capped.ActiveJobs = 2
	f := newFixture(
		[]*Node{capped, proxyNode(2, "http://proxy-2", nil)},
		[]*Node{},
	)

	for i := 0; i < 3; i++ {
		plan := f.planner.PlanSession("s", "", false, 0)
		if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-2" {
			t.Fatalf("expected proxy-2 (proxy-1 at cap), got %+v", plan.ProxyNode)
		}
	}
}

func TestGroupAtProxyCapacityExcludesGroupTranscode(t *testing.T) {
	groupProxy := proxyNode(1, "http://proxy-a", strPtr("rack-a"))
	groupProxy.MaxJobs = intPtr(1)
	groupProxy.ActiveJobs = 1
	f := newFixture(
		[]*Node{groupProxy, proxyNode(2, "http://proxy-1", nil)},
		[]*Node{
			transcodeNode(3, "http://tc-a", strPtr("rack-a"), 0),
			transcodeNode(4, "http://tc-1", nil, 7),
		},
	)

	// rack-a's only proxy is full, so its transcode node must not be used —
	// streams pinned to rack-a would have nowhere to go.
	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-1" {
		t.Fatalf("expected ungrouped tc-1, got %+v", plan.TranscodeNode)
	}
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-1" {
		t.Fatalf("expected ungrouped proxy-1, got %+v", plan.ProxyNode)
	}
}

func TestGroupProxyReservationsGateGroupCapacity(t *testing.T) {
	groupProxy := proxyNode(1, "http://proxy-a", strPtr("rack-a"))
	groupProxy.MaxJobs = intPtr(1)
	f := newFixture(
		[]*Node{groupProxy},
		[]*Node{transcodeNode(2, "http://tc-a", strPtr("rack-a"), 0)},
	)

	// The first session reserves the group's only proxy slot.
	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.ProxyNode == nil {
		t.Fatalf("first session should get both nodes, got %+v", plan)
	}

	// With the group's proxy fully reserved, its transcode node is
	// ineligible too — streams pinned to the group would have nowhere to go.
	plan = f.planner.PlanSession("s2", "", true, 0)
	if plan.TranscodeNode != nil || plan.ProxyNode != nil {
		t.Fatalf("second session should be rejected, got %+v", plan)
	}
}

func TestReservationsCountTowardCaps(t *testing.T) {
	capped := transcodeNode(2, "http://tc-1", nil, 0)
	capped.MaxJobs = intPtr(2)
	lastCheck := time.Date(2026, 6, 10, 11, 59, 0, 0, time.UTC)
	capped.LastHealthCheck = &lastCheck
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{capped},
	)

	// Two sessions fill the cap via reservations before any health refresh.
	if f.planner.PlanSession("s1", "", true, 0).TranscodeNode == nil {
		t.Fatal("first session should be admitted")
	}
	if f.planner.PlanSession("s2", "", true, 0).TranscodeNode == nil {
		t.Fatal("second session should be admitted")
	}
	if got := f.planner.PlanSession("s3", "", true, 0).TranscodeNode; got != nil {
		t.Fatalf("third session should be rejected, got %+v", got)
	}

	// Re-planning an admitted session must not double-count it.
	if f.planner.PlanSession("s2", "http://tc-1", true, 0).TranscodeNode == nil {
		t.Fatal("re-plan of s2 should be admitted")
	}

	// A health report newer than the reservations becomes authoritative:
	// the node now says 1 job, so one slot is free again.
	newer := f.now.Add(10 * time.Second)
	capped.LastHealthCheck = &newer
	capped.ActiveJobs = 1
	f.now = f.now.Add(20 * time.Second)
	if f.planner.PlanSession("s4", "", true, 0).TranscodeNode == nil {
		t.Fatal("session should be admitted after fresh health report")
	}
}

func TestReservationsExpire(t *testing.T) {
	capped := transcodeNode(2, "http://tc-1", nil, 0)
	capped.MaxJobs = intPtr(1)
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{capped},
	)

	if f.planner.PlanSession("s1", "", true, 0).TranscodeNode == nil {
		t.Fatal("first session should be admitted")
	}
	if got := f.planner.PlanSession("s2", "", true, 0).TranscodeNode; got != nil {
		t.Fatalf("second session should be rejected, got %+v", got)
	}

	// Without health reports (LastHealthCheck nil) reservations still expire
	// after maxReservationAge so a stalled health checker can't wedge admission.
	f.now = f.now.Add(maxReservationAge + time.Second)
	if f.planner.PlanSession("s3", "", true, 0).TranscodeNode == nil {
		t.Fatal("session should be admitted after reservation expiry")
	}
}

func TestDirectPlayIgnoresGroups(t *testing.T) {
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-a", strPtr("rack-a"))},
		[]*Node{},
	)

	plan := f.planner.PlanSession("s1", "", false, 0)
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-a" {
		t.Fatalf("expected grouped proxy to serve direct play, got %+v", plan.ProxyNode)
	}
	if plan.TranscodeNode != nil {
		t.Fatalf("direct play must not pick a transcode node, got %+v", plan.TranscodeNode)
	}
}

func TestGroupRoundRobinAcrossGroupProxies(t *testing.T) {
	f := newFixture(
		[]*Node{
			proxyNode(1, "http://proxy-a1", strPtr("rack-a")),
			proxyNode(2, "http://proxy-a2", strPtr("rack-a")),
		},
		[]*Node{transcodeNode(3, "http://tc-a", strPtr("rack-a"), 0)},
	)

	seen := map[string]bool{}
	for i, id := range []string{"s1", "s2"} {
		plan := f.planner.PlanSession(id, "", true, 0)
		if plan.ProxyNode == nil {
			t.Fatalf("plan %d: expected a proxy", i)
		}
		seen[plan.ProxyNode.URL] = true
	}
	if len(seen) != 2 {
		t.Fatalf("expected round-robin across both group proxies, saw %v", seen)
	}
}

func TestNilPlannerReturnsEmptyPlan(t *testing.T) {
	var p *Planner
	plan := p.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode != nil || plan.ProxyNode != nil {
		t.Fatalf("expected empty plan from nil planner, got %+v", plan)
	}
}

func TestBandwidthCapSkipsSaturatedProxy(t *testing.T) {
	saturated := proxyNode(1, "http://proxy-1", nil)
	saturated.MaxBandwidthKbps = intPtr(100_000) // 100 Mbps
	saturated.EgressKbps = 97_000
	f := newFixture(
		[]*Node{saturated, proxyNode(2, "http://proxy-2", nil)},
		[]*Node{},
	)

	// A 6 Mbps stream doesn't fit in proxy-1's 3 Mbps of headroom.
	for i := 0; i < 3; i++ {
		plan := f.planner.PlanSession("s", "", false, 6_000)
		if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-2" {
			t.Fatalf("expected proxy-2 (proxy-1 saturated), got %+v", plan.ProxyNode)
		}
	}

	// A 2 Mbps stream still fits.
	plan := f.planner.PlanSession("s2", "", false, 2_000)
	if plan.ProxyNode == nil {
		t.Fatal("expected a proxy for a stream that fits")
	}
}

func TestBandwidthReservationsCountDuringBridge(t *testing.T) {
	capped := proxyNode(1, "http://proxy-1", nil)
	capped.MaxBandwidthKbps = intPtr(10_000)
	f := newFixture([]*Node{capped}, []*Node{})

	// Two 4 Mbps admissions fit; the third would exceed the 10 Mbps cap
	// because the first two are still bridged as reservations.
	if f.planner.PlanSession("s1", "", false, 4_000).ProxyNode == nil {
		t.Fatal("first stream should be admitted")
	}
	if f.planner.PlanSession("s2", "", false, 4_000).ProxyNode == nil {
		t.Fatal("second stream should be admitted")
	}
	if got := f.planner.PlanSession("s3", "", false, 4_000).ProxyNode; got != nil {
		t.Fatalf("third stream should be rejected, got %+v", got)
	}

	// Unlike job reservations, bandwidth bridges ignore health freshness —
	// a report right after admission would not reflect the streams yet.
	newer := f.now.Add(5 * time.Second)
	f.proxies.ApplyHealth(1, true, 0, 0, newer)
	f.now = f.now.Add(10 * time.Second)
	if got := f.planner.PlanSession("s4", "", false, 4_000).ProxyNode; got != nil {
		t.Fatalf("stream should still be rejected during bridge window, got %+v", got)
	}

	// After the bridge window the measured egress is authoritative. The
	// meter now reports 8 Mbps, so one more 4 Mbps stream still won't fit,
	// but a 2 Mbps one will.
	f.now = f.now.Add(bandwidthBridgeAge)
	f.proxies.ApplyHealth(1, true, 0, 8_000, f.now)
	if got := f.planner.PlanSession("s5", "", false, 4_000).ProxyNode; got != nil {
		t.Fatalf("4 Mbps stream should not fit at 8/10 Mbps, got %+v", got)
	}
	if f.planner.PlanSession("s6", "", false, 2_000).ProxyNode == nil {
		t.Fatal("2 Mbps stream should fit at 8/10 Mbps")
	}
}

func TestGroupBandwidthGatesGroupTranscode(t *testing.T) {
	groupProxy := proxyNode(1, "http://proxy-a", strPtr("rack-a"))
	groupProxy.MaxBandwidthKbps = intPtr(10_000)
	groupProxy.EgressKbps = 9_000
	f := newFixture(
		[]*Node{groupProxy, proxyNode(2, "http://proxy-1", nil)},
		[]*Node{
			transcodeNode(3, "http://tc-a", strPtr("rack-a"), 0),
			transcodeNode(4, "http://tc-1", nil, 7),
		},
	)

	// rack-a's proxy has no bandwidth headroom for a 4 Mbps stream, so the
	// group's idle transcode node must be skipped.
	plan := f.planner.PlanSession("s1", "", true, 4_000)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-1" {
		t.Fatalf("expected ungrouped tc-1, got %+v", plan.TranscodeNode)
	}
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-1" {
		t.Fatalf("expected ungrouped proxy-1, got %+v", plan.ProxyNode)
	}

	// A 500 kbps stream fits and stays pinned to the group.
	plan = f.planner.PlanSession("s2", "", true, 500)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-a" {
		t.Fatalf("expected rack-a tc-a for small stream, got %+v", plan.TranscodeNode)
	}
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-a" {
		t.Fatalf("expected rack-a proxy, got %+v", plan.ProxyNode)
	}
}

func TestUnknownBitrateAdmittedBelowCap(t *testing.T) {
	p := proxyNode(1, "http://proxy-1", nil)
	p.MaxBandwidthKbps = intPtr(10_000)
	p.EgressKbps = 9_999
	f := newFixture([]*Node{p}, []*Node{})

	// Unknown bitrate (0): admitted while measured egress is below the cap.
	if f.planner.PlanSession("s1", "", false, 0).ProxyNode == nil {
		t.Fatal("unknown-bitrate stream should be admitted below cap")
	}

	f.proxies.ApplyHealth(1, true, 0, 10_000, f.now)
	if got := f.planner.PlanSession("s2", "", false, 0).ProxyNode; got != nil {
		t.Fatalf("unknown-bitrate stream should be rejected at cap, got %+v", got)
	}
}

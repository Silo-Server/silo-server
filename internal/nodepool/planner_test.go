package nodepool

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

func TestTranscodeNodeHealthyNormalizesTrailingSlash(t *testing.T) {
	f := newFixture(nil, []*Node{
		{URL: "http://tc-node:8080/", Enabled: true, Healthy: true},
	})

	if !f.planner.TranscodeNodeHealthy("http://tc-node:8080") {
		t.Fatal("stored trailing-slash URL did not match a lookup without the slash")
	}
	if !f.planner.TranscodeNodeHealthy("http://tc-node:8080/") {
		t.Fatal("stored trailing-slash URL did not match a lookup with the slash")
	}
	if f.planner.TranscodeNodeHealthy("http://other-node:8080") {
		t.Fatal("unknown node URL reported healthy")
	}
	if f.planner.TranscodeNodeHealthy("") {
		t.Fatal("empty node URL reported healthy")
	}
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

func TestPlanSessionWithRestrictsEligibleTranscodeNodes(t *testing.T) {
	f := newFixture(nil, []*Node{
		transcodeNode(1, "http://tc-a", nil, 0),
		transcodeNode(2, "http://tc-b", nil, 5),
	})
	eligible := func(n *Node) bool { return n != nil && n.URL == "http://tc-b" }

	plan := f.planner.PlanSessionWith("s1", "", true, 0, eligible)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-b" {
		t.Fatalf("expected the eligible node despite its higher load, got %+v", plan.TranscodeNode)
	}
	if none := f.planner.PlanSessionWith("s2", "", true, 0, func(*Node) bool { return false }); none.TranscodeNode != nil {
		t.Fatalf("no eligible node must select nothing, got %+v", none.TranscodeNode)
	}
	// Soft affinity to the session's current node must not survive the
	// current node becoming ineligible.
	if sticky := f.planner.PlanSessionWith("s3", "http://tc-a", true, 0, eligible); sticky.TranscodeNode == nil || sticky.TranscodeNode.URL != "http://tc-b" {
		t.Fatalf("affinity to an ineligible node must yield to an eligible one, got %+v", sticky.TranscodeNode)
	}
	if unrestricted := f.planner.PlanSessionWith("s4", "", true, 0, nil); unrestricted.TranscodeNode == nil || unrestricted.TranscodeNode.URL != "http://tc-a" {
		t.Fatalf("nil predicate must behave like PlanSession, got %+v", unrestricted.TranscodeNode)
	}
}

func TestPlanTranscodeSessionWithLocalEgressDoesNotUseProxyCapacity(t *testing.T) {
	group := strPtr("rack-a")
	proxy := proxyNode(1, "http://proxy-a", group)
	proxy.Healthy = false
	transcode := transcodeNode(2, "http://tc-a", group, 0)
	f := newFixture([]*Node{proxy}, []*Node{transcode})

	plan := f.planner.PlanTranscodeSessionWithLocalEgress("s-local-egress", "", func(node *Node) bool {
		return node != nil && node.URL == transcode.URL
	})
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != transcode.URL {
		t.Fatalf("local-egress plan = %#v, want healthy transcode despite unrelated proxy health", plan)
	}
	if plan.ProxyNode != nil {
		t.Fatalf("local-egress plan exposed proxy %#v", plan.ProxyNode)
	}
	reservation := f.planner.reserved["s-local-egress"]
	if reservation == nil || reservation.transcodeURL != transcode.URL || reservation.proxyURL != "" || reservation.kbps != 0 {
		t.Fatalf("local-egress reservation = %#v, want transcode-only accounting", reservation)
	}

	if none := f.planner.PlanTranscodeSessionWithLocalEgress("s-ineligible", "", func(*Node) bool { return false }); none.TranscodeNode != nil {
		t.Fatalf("ineligible local-egress plan selected %#v", none.TranscodeNode)
	}
	if _, reserved := f.planner.reserved["s-ineligible"]; reserved {
		t.Fatal("ineligible local-egress plan left a reservation")
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

// A start that selected both nodes but publishes a URL the proxy does not serve
// must give the proxy's job slot and estimated bandwidth back while the
// transcode node keeps running the job. Asserted through selection, which is
// what the accounting exists to drive.
func TestReleaseSessionProxyFreesTheProxyHalfAndKeepsTheTranscode(t *testing.T) {
	proxy := proxyNode(1, "http://proxy-1", nil)
	proxy.MaxJobs = intPtr(1)
	proxy.MaxBandwidthKbps = intPtr(10_000)
	transcode := transcodeNode(2, "http://tc-1", nil, 0)
	transcode.MaxJobs = intPtr(1)
	f := newFixture([]*Node{proxy}, []*Node{transcode})

	plan := f.planner.PlanSession("s1", "", true, 8_000)
	if plan.TranscodeNode == nil || plan.ProxyNode == nil {
		t.Fatalf("plan = %+v, want both halves reserved", plan)
	}
	// Both halves are charged, so nothing else fits on the proxy.
	if got := f.planner.PlanSession("s2", "", false, 2_000).ProxyNode; got != nil {
		t.Fatalf("proxy admitted %+v while its reservation stands", got)
	}

	f.planner.ReleaseSessionProxy("s1")

	// 8 Mbps only fits if BOTH the job slot and the bandwidth charge were
	// released; the estimate alone would leave 2 Mbps of headroom.
	if got := f.planner.PlanSession("s2", "", false, 8_000).ProxyNode; got == nil {
		t.Fatal("released proxy half still blocked the proxy")
	}
	// The transcode node is still running s1, so its slot is still charged.
	if got := f.planner.PlanSession("s3", "", true, 0).TranscodeNode; got != nil {
		t.Fatalf("transcode node admitted %+v; only the proxy half was released", got)
	}

	// Nil-safe, and an unknown session is a no-op rather than a phantom entry.
	var absent *Planner
	absent.ReleaseSessionProxy("s1")
	f.planner.ReleaseSessionProxy("never-planned")
	if _, ok := f.planner.reserved["never-planned"]; ok {
		t.Fatal("releasing an unknown session created a reservation")
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

func TestTranscodeWorkAvailableIgnoresExpiredReservations(t *testing.T) {
	capped := transcodeNode(2, "http://tc-1", nil, 0)
	capped.MaxJobs = intPtr(1)
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{capped},
	)

	if f.planner.PlanSession("s1", "", true, 0).TranscodeNode == nil {
		t.Fatal("first session should be admitted")
	}
	if f.planner.TranscodeWorkAvailableWith(nil) {
		t.Fatal("fresh reservation should consume the only transcode slot")
	}

	// Availability checks are intentionally read-only, but expired reservations
	// must not consume capacity while waiting for a later placement to prune them.
	f.now = f.now.Add(maxReservationAge + time.Second)
	if !f.planner.TranscodeWorkAvailableWith(nil) {
		t.Fatal("expired reservation should not consume transcode capacity")
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
	f.proxies.ApplyHealth(1, f.proxies.Nodes()[0].URL, true, 0, 0, "", nil, newer)
	f.now = f.now.Add(10 * time.Second)
	if got := f.planner.PlanSession("s4", "", false, 4_000).ProxyNode; got != nil {
		t.Fatalf("stream should still be rejected during bridge window, got %+v", got)
	}

	// After the bridge window the measured egress is authoritative. The
	// meter now reports 8 Mbps, so one more 4 Mbps stream still won't fit,
	// but a 2 Mbps one will.
	f.now = f.now.Add(bandwidthBridgeAge)
	f.proxies.ApplyHealth(1, f.proxies.Nodes()[0].URL, true, 0, 8_000, "", nil, f.now)
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

	f.proxies.ApplyHealth(1, f.proxies.Nodes()[0].URL, true, 0, 10_000, "", nil, f.now)
	if got := f.planner.PlanSession("s2", "", false, 0).ProxyNode; got != nil {
		t.Fatalf("unknown-bitrate stream should be rejected at cap, got %+v", got)
	}
}

func TestReserveTranscodeWorkSharesCapacityReservations(t *testing.T) {
	capOne := 1
	transcodes := NewTranscodePool()
	transcodes.SetNodes([]*Node{
		{URL: "http://transcode-a", Enabled: true, Healthy: true, MaxJobs: &capOne},
		{URL: "http://transcode-b", Enabled: true, Healthy: true, MaxJobs: &capOne},
	})
	planner := NewPlanner(NewProxyPool(), transcodes)

	first, releaseFirst := planner.ReserveTranscodeWork("download-1")
	if first == nil || first.URL != "http://transcode-a" {
		t.Fatalf("first node = %+v", first)
	}
	second, releaseSecond := planner.ReserveTranscodeWork("download-2")
	if second == nil || second.URL != "http://transcode-b" {
		t.Fatalf("second node = %+v", second)
	}
	if third, _ := planner.ReserveTranscodeWork("download-3"); third != nil {
		t.Fatalf("third node = %+v, want no capacity", third)
	}

	releaseFirst()
	third, releaseThird := planner.ReserveTranscodeWork("download-3")
	if third == nil || third.URL != "http://transcode-a" {
		t.Fatalf("third node after release = %+v", third)
	}
	releaseSecond()
	releaseThird()
}

func TestReserveTranscodeWorkOverlappingAttemptsReleaseOnlyTheirOwnReservation(t *testing.T) {
	capTwo := 2
	transcodes := NewTranscodePool()
	transcodes.SetNodes([]*Node{{URL: "http://transcode-a", Enabled: true, Healthy: true, MaxJobs: &capTwo}})
	planner := NewPlanner(NewProxyPool(), transcodes)

	first, releaseFirst := planner.ReserveTranscodeWork("same-artifact")
	second, releaseSecond := planner.ReserveTranscodeWork("same-artifact")
	if first == nil || second == nil {
		t.Fatalf("overlapping reservations = first %+v second %+v", first, second)
	}
	if third, _ := planner.ReserveTranscodeWork("third-attempt"); third != nil {
		t.Fatalf("third reservation = %+v, want full node", third)
	}

	releaseFirst()
	third, releaseThird := planner.ReserveTranscodeWork("third-attempt")
	if third == nil {
		t.Fatal("first release did not free its own reservation")
	}
	if fourth, _ := planner.ReserveTranscodeWork("fourth-attempt"); fourth != nil {
		t.Fatalf("second overlapping reservation was lost: fourth = %+v", fourth)
	}

	releaseSecond()
	releaseThird()
}

func TestPlanDownloadSkipsBandwidthCappedProxiesAndReservesJobCapacity(t *testing.T) {
	bandwidthCap := 100_000
	jobCap := 1
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{
		{URL: "http://bandwidth-capped", Enabled: true, Healthy: true, MaxBandwidthKbps: &bandwidthCap},
		{URL: "http://uncapped", Enabled: true, Healthy: true, MaxJobs: &jobCap},
	})
	planner := NewPlanner(proxies, NewTranscodePool())

	first := planner.PlanDownload("download-1")
	if first.ProxyNode == nil || first.ProxyNode.URL != "http://uncapped" {
		t.Fatalf("first plan = %+v", first)
	}
	if second := planner.PlanDownload("download-2"); second.ProxyNode != nil {
		t.Fatalf("second plan = %+v, want job-cap rejection", second)
	}
	planner.ReleaseSession("download-1")
	if second := planner.PlanDownload("download-2"); second.ProxyNode == nil {
		t.Fatal("download was not admitted after reservation release")
	}
}

func TestPlanDownloadPrefersArtifactOriginGroup(t *testing.T) {
	groupA, groupB := "host-a", "host-b"
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{
		{URL: "http://proxy-a", Group: &groupA, Enabled: true, Healthy: true},
		{URL: "http://proxy-b", Group: &groupB, Enabled: true, Healthy: true},
	})
	planner := NewPlanner(proxies, NewTranscodePool())
	plan := planner.PlanDownload("download-grouped", groupB)
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-b" {
		t.Fatalf("plan = %+v, want proxy-b", plan)
	}
}

func TestPlanDownloadFallsBackWhenOriginGroupHasNoProxy(t *testing.T) {
	group := "host-a"
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{{URL: "http://proxy-a", Group: &group, Enabled: true, Healthy: true}})
	planner := NewPlanner(proxies, NewTranscodePool())
	plan := planner.PlanDownload("download-fallback", "host-missing")
	if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-a" {
		t.Fatalf("plan = %+v, want proxy-a fallback", plan)
	}
}

// A proxy-only plan applies the eligibility predicate to the proxy: it is the
// node that executes the recipe. Without this, one incapable round-robin pick
// would abandon a pool that still holds a capable sibling.
func TestPlanSessionWithFiltersProxiesForProxyOnlyPlans(t *testing.T) {
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{
		{ID: 1, URL: "http://proxy-old", Enabled: true, Healthy: true},
		{ID: 2, URL: "http://proxy-new", Enabled: true, Healthy: true},
	})
	planner := NewPlanner(proxies, NewTranscodePool())

	// Only the upgraded proxy can run the recipe; every selection must land
	// there regardless of where the round-robin cursor happens to be.
	for i := 0; i < 4; i++ {
		plan := planner.PlanSessionWith("session-"+strconv.Itoa(i), "", false, 0, func(n *Node) bool {
			return n.URL == "http://proxy-new"
		})
		if plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-new" {
			t.Fatalf("selection %d = %#v, want the capable proxy", i, plan.ProxyNode)
		}
	}
}

func TestPlanSessionWithReturnsNoProxyWhenNoneAreCapable(t *testing.T) {
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{{ID: 1, URL: "http://proxy-old", Enabled: true, Healthy: true}})
	planner := NewPlanner(proxies, NewTranscodePool())

	plan := planner.PlanSessionWith("session-none", "", false, 0, func(*Node) bool { return false })
	if plan.ProxyNode != nil {
		t.Fatalf("proxy = %#v, want none when the pool cannot execute the recipe", plan.ProxyNode)
	}
	// An empty plan must not leave a reservation pinning capacity.
	if _, reserved := planner.reserved["session-none"]; reserved {
		t.Fatal("an unsatisfiable plan left a reservation behind")
	}
}

func TestProxyNodeURLsListsEnabledProxies(t *testing.T) {
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{
		{ID: 1, URL: "http://proxy-1", Enabled: true, Healthy: true},
		{ID: 2, URL: "http://proxy-2", Enabled: true, Healthy: false},
	})
	planner := NewPlanner(proxies, NewTranscodePool())

	// Unhealthy nodes are still listed: capability planning wants the
	// deployment's toolchain, and an unreachable node excludes itself when its
	// capability fetch fails.
	urls := planner.ProxyNodeURLs()
	if len(urls) != 2 {
		t.Fatalf("proxy urls = %v, want both pooled proxies", urls)
	}
}

// gpuCapabilities is a capability report naming one render device per uuid, so
// pool loading derives exactly those identities. Written as a payload rather
// than by setting the derived field directly: the pool re-derives it on load,
// which is the behavior these tests depend on.
func gpuCapabilities(uuids ...string) json.RawMessage {
	devices := make([]string, 0, len(uuids))
	for i, uuid := range uuids {
		devices = append(devices, fmt.Sprintf(`{"path":"/dev/dri/renderD%d","gpu_uuid":%q}`, 128+i, uuid))
	}
	return json.RawMessage(`{"boot_id":"boot-1","render_device_details":[` + strings.Join(devices, ",") + `]}`)
}

// gpuTranscodeNode is transcodeNode carrying a capability report for the named
// GPUs.
func gpuTranscodeNode(id int, url string, activeJobs int, uuids ...string) *Node {
	n := transcodeNode(id, url, nil, activeJobs)
	n.Capabilities = gpuCapabilities(uuids...)
	return n
}

// Two pooled nodes can be two containers on one card. Spreading jobs evenly
// across node records that share silicon does not spread the work, so equal job
// counts are broken toward the node whose physical GPU is doing less.
func TestEqualJobsPrefersTheIdlePhysicalGPU(t *testing.T) {
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{
			gpuTranscodeNode(2, "http://tc-a", 2, "GPU-shared"),
			gpuTranscodeNode(3, "http://tc-b", 0, "GPU-shared"),
			gpuTranscodeNode(4, "http://tc-c", 0, "GPU-own"),
		},
	)

	// tc-b and tc-c are level on jobs and tc-b comes first in pool order, so
	// only the shared-GPU tie-break can select tc-c.
	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-c" {
		t.Fatalf("expected tc-c on the idle GPU, got %+v", plan.TranscodeNode)
	}

	// The same rule applies to the API-relayed route, which shares pickNode.
	// s1's reservation is released first so the second pick starts from the
	// same job counts rather than from tc-c already charged for s1.
	f.planner.ReleaseSession("s1")
	local := f.planner.PlanTranscodeSessionWithLocalEgress("s2", "", nil)
	if local.TranscodeNode == nil || local.TranscodeNode.URL != "http://tc-c" {
		t.Fatalf("local-egress expected tc-c, got %+v", local.TranscodeNode)
	}
}

// Jobs occupy a card whether or not the node running them may take another, so
// an unhealthy sharer — which stays pooled, only ineligible — still counts
// against its group.
func TestSharedGPULoadCountsUnusableSharers(t *testing.T) {
	busy := gpuTranscodeNode(2, "http://tc-a", 3, "GPU-shared")
	busy.Healthy = false
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{
			busy,
			gpuTranscodeNode(3, "http://tc-b", 0, "GPU-shared"),
			gpuTranscodeNode(4, "http://tc-c", 0, "GPU-own"),
		},
	)

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-c" {
		t.Fatalf("expected tc-c, got %+v", plan.TranscodeNode)
	}
}

// Boot id detection is best-effort, so two unrelated hosts can each report an
// iGPU at the near-universal slot 0000:00:02.0 with no boot id to scope it.
// Those are two cards, and planning them as one would steer work away from a
// genuinely idle GPU.
func TestUnscopedSlotsDoNotShareAGroup(t *testing.T) {
	iGPU := json.RawMessage(`{"render_device_details":[` +
		`{"path":"/dev/dri/renderD128","pci_address":"0000:00:02.0"}]}`)
	busy := transcodeNode(2, "http://tc-a", nil, 2)
	busy.Capabilities = iGPU
	idle := transcodeNode(3, "http://tc-b", nil, 0)
	idle.Capabilities = iGPU
	f := newFixture([]*Node{proxyNode(1, "http://proxy-1", nil)}, []*Node{busy, idle})

	if got := f.transcodes.Nodes()[0].PhysicalGPUKeys; got != nil {
		t.Fatalf("an unscoped slot derived %v, want no key", got)
	}
	loads := f.planner.physicalGPULoadScore(f.transcodes.Nodes(), f.now)
	if got := loads(f.transcodes.Nodes()[1]); got != 0 {
		t.Fatalf("idle host's shared-GPU load = %d, want 0 (its own jobs only)", got)
	}
}

// A node sharing several keys with the same peer must not have that peer's jobs
// counted once per key; the group is a set of nodes, not of keys.
func TestSharedGPULoadCountsEachSharerOnce(t *testing.T) {
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{
			gpuTranscodeNode(2, "http://tc-a", 1, "GPU-x", "GPU-y"),
			gpuTranscodeNode(3, "http://tc-b", 0, "GPU-x", "GPU-y"),
			gpuTranscodeNode(4, "http://tc-c", 0, "GPU-p", "GPU-q"),
		},
	)
	// tc-b's group load is 1 (its own 0 plus tc-a's single job counted once),
	// tc-c's is 0, so tc-c wins — but only by one job, which double counting
	// would inflate without changing the winner. The assertion that matters is
	// the score itself.
	loads := f.planner.physicalGPULoadScore(f.transcodes.Nodes(), f.now)
	if got := loads(f.transcodes.Nodes()[1]); got != 1 {
		t.Fatalf("tc-b shared-GPU load = %d, want 1", got)
	}
	if got := loads(f.transcodes.Nodes()[0]); got != 1 {
		t.Fatalf("tc-a shared-GPU load = %d, want 1", got)
	}
	if got := loads(f.transcodes.Nodes()[2]); got != 0 {
		t.Fatalf("tc-c shared-GPU load = %d, want 0", got)
	}
}

// The tie-break only ranks candidates that are already level; a node with fewer
// jobs still wins outright, even when its GPU is the busier one.
func TestFewerJobsBeatsAnIdlePhysicalGPU(t *testing.T) {
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{
			gpuTranscodeNode(2, "http://tc-a", 5, "GPU-shared"),
			gpuTranscodeNode(3, "http://tc-b", 0, "GPU-shared"),
			gpuTranscodeNode(4, "http://tc-c", 1, "GPU-own"),
		},
	)

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-b" {
		t.Fatalf("expected the least-loaded tc-b, got %+v", plan.TranscodeNode)
	}
}

// Soft affinity outranks the tie-break: moving a running session to another
// node costs a restart, which a shared GPU is not on its own reason enough for.
func TestSharedGPUTieBreakDoesNotBreakSoftAffinity(t *testing.T) {
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{
			gpuTranscodeNode(2, "http://tc-a", 2, "GPU-shared"),
			gpuTranscodeNode(3, "http://tc-b", 0, "GPU-shared"),
			gpuTranscodeNode(4, "http://tc-c", 0, "GPU-own"),
		},
	)

	plan := f.planner.PlanSession("s1", "http://tc-b", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-b" {
		t.Fatalf("expected affinity to keep tc-b, got %+v", plan.TranscodeNode)
	}
}

// A node with no identifiable GPU is a group of itself, so a pool that reports
// none selects exactly as it did before this rule existed: least jobs, then
// pool order.
func TestNodesWithoutGPUKeysKeepPoolOrderOnTies(t *testing.T) {
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-1", nil)},
		[]*Node{
			transcodeNode(2, "http://tc-a", nil, 0),
			transcodeNode(3, "http://tc-b", nil, 0),
		},
	)

	plan := f.planner.PlanSession("s1", "", true, 0)
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-a" {
		t.Fatalf("expected the first pooled node, got %+v", plan.TranscodeNode)
	}
}

// Proxies are picked round-robin, and egress is not GPU work: a shared card
// must not perturb that rotation.
func TestSharedGPUDoesNotAffectProxySelection(t *testing.T) {
	busyPeer := proxyNode(1, "http://proxy-a", nil)
	busyPeer.Capabilities = gpuCapabilities("GPU-shared")
	busyPeer.ActiveJobs = 9
	quiet := proxyNode(2, "http://proxy-b", nil)
	quiet.Capabilities = gpuCapabilities("GPU-shared")
	f := newFixture([]*Node{busyPeer, quiet}, nil)

	first := f.planner.PlanSession("s1", "", false, 0).ProxyNode
	second := f.planner.PlanSession("s2", "", false, 0).ProxyNode
	if first == nil || second == nil || first.URL == second.URL {
		t.Fatalf("expected round-robin across both proxies, got %+v then %+v", first, second)
	}
}

// A proxy URL stored with a trailing slash must still resolve: URLs are
// normalized where they enter the pool, so the normalized lookup key
// ProxyNodeByURL builds compares equal. Without the SetNodes normalization the
// lookup always missed and capability pricing fell back to the cluster policy
// for exactly the proxies an operator had configured by hand.
func TestProxyNodeByURLNormalizesStoredAndLookupURLs(t *testing.T) {
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{{ID: 1, Name: "p1", URL: "https://proxy.example.com/", Enabled: true, Healthy: true}})
	planner := NewPlanner(proxies, NewTranscodePool())

	for _, lookup := range []string{"https://proxy.example.com", "https://proxy.example.com/"} {
		node, ok := planner.ProxyNodeByURL(lookup)
		if !ok || node == nil {
			t.Fatalf("ProxyNodeByURL(%q) = %v, %v; want the pooled node", lookup, node, ok)
		}
		if node.ID != 1 {
			t.Fatalf("ProxyNodeByURL(%q) resolved node %d, want 1", lookup, node.ID)
		}
	}
}

// ClientURL is what every client-facing URL builder joins paths onto: the
// public URL when set, the backend URL otherwise. The fallback is what keeps
// every deployment registered before the split — and every flat network —
// byte-identical.
func TestClientURLPrefersThePublicURL(t *testing.T) {
	public := "https://cdn.example.com/"
	blank := "   "
	cases := []struct {
		name string
		node *Node
		want string
	}{
		{"nil node", nil, ""},
		{"no public url", &Node{URL: "http://10.0.0.5:8083/"}, "http://10.0.0.5:8083"},
		{"public url set", &Node{URL: "http://10.0.0.5:8083", PublicURL: &public}, "https://cdn.example.com"},
		{"blank public url falls back", &Node{URL: "http://10.0.0.5:8083", PublicURL: &blank}, "http://10.0.0.5:8083"},
	}
	for _, tc := range cases {
		if got := tc.node.ClientURL(); got != tc.want {
			t.Fatalf("%s: ClientURL() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

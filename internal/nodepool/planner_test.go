package nodepool

import (
	"encoding/json"
	"fmt"
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
	tp := newKeyedTranscodePool()
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

const unchecked = "-"

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
	f.proxies.ApplyHealth(1, f.proxies.Nodes()[0].URL, true, 0, 0, "", nil, nil, newer)
	f.now = f.now.Add(10 * time.Second)
	if got := f.planner.PlanSession("s4", "", false, 4_000).ProxyNode; got != nil {
		t.Fatalf("stream should still be rejected during bridge window, got %+v", got)
	}

	// After the bridge window the measured egress is authoritative. The
	// meter now reports 8 Mbps, so one more 4 Mbps stream still won't fit,
	// but a 2 Mbps one will.
	f.now = f.now.Add(bandwidthBridgeAge)
	f.proxies.ApplyHealth(1, f.proxies.Nodes()[0].URL, true, 0, 8_000, "", nil, nil, f.now)
	if got := f.planner.PlanSession("s5", "", false, 4_000).ProxyNode; got != nil {
		t.Fatalf("4 Mbps stream should not fit at 8/10 Mbps, got %+v", got)
	}
	if f.planner.PlanSession("s6", "", false, 2_000).ProxyNode == nil {
		t.Fatal("2 Mbps stream should fit at 8/10 Mbps")
	}
}

func TestReserveTranscodeWorkSharesCapacityReservations(t *testing.T) {
	capOne := 1
	transcodes := newKeyedTranscodePool()
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
	transcodes := newKeyedTranscodePool()
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
	planner := NewPlanner(proxies, newKeyedTranscodePool())

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

func TestPlanRouteReservesExactlyRequestedTopology(t *testing.T) {
	group := "rack-a"
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{{ID: 1, URL: "http://proxy", Group: &group, Enabled: true, Healthy: true}})
	transcodes := newKeyedTranscodePool()
	transcodes.SetNodes([]*Node{{ID: 2, URL: "http://transcode", Group: &group, Enabled: true, Healthy: true}})
	planner := NewPlanner(proxies, transcodes)

	tests := []struct {
		name          string
		request       RouteRequest
		wantTranscode bool
		wantProxy     bool
	}{
		{"neither", RouteRequest{SessionID: "none"}, false, false},
		{"proxy", RouteRequest{SessionID: "proxy", NeedsProxy: true}, false, true},
		{"transcode", RouteRequest{SessionID: "transcode", NeedsTranscode: true}, true, false},
		{"both", RouteRequest{SessionID: "both", NeedsTranscode: true, NeedsProxy: true}, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := planner.PlanRoute(test.request)
			if (plan.TranscodeNode != nil) != test.wantTranscode || (plan.ProxyNode != nil) != test.wantProxy {
				t.Fatalf("plan = %#v, want transcode=%t proxy=%t", plan, test.wantTranscode, test.wantProxy)
			}
			reservation, reserved := planner.reserved[test.request.SessionID]
			if !test.wantTranscode && !test.wantProxy {
				if reserved {
					t.Fatalf("neither route left reservation %#v", reservation)
				}
				return
			}
			if !reserved || (reservation.transcodeURL != "") != test.wantTranscode || (reservation.proxyURL != "") != test.wantProxy {
				t.Fatalf("reservation = %#v, want exact route halves", reservation)
			}
		})
	}
}

func TestPlanRouteUsesIndependentCapabilityPredicates(t *testing.T) {
	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{
		{ID: 1, URL: "http://proxy-old", Enabled: true, Healthy: true},
		{ID: 2, URL: "http://proxy-new", Enabled: true, Healthy: true},
	})
	transcodes := newKeyedTranscodePool()
	transcodes.SetNodes([]*Node{
		{ID: 3, URL: "http://transcode-old", Enabled: true, Healthy: true},
		{ID: 4, URL: "http://transcode-new", Enabled: true, Healthy: true},
	})
	planner := NewPlanner(proxies, transcodes)
	plan := planner.PlanRoute(RouteRequest{
		SessionID: "capable", NeedsTranscode: true, NeedsProxy: true,
		TranscodeEligible: func(node *Node) bool { return node.URL == "http://transcode-new" },
		ProxyEligible:     func(node *Node) bool { return node.URL == "http://proxy-new" },
	})
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://transcode-new" ||
		plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-new" {
		t.Fatalf("plan = %#v, want independently capable nodes", plan)
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

// A node the caller found unreachable counts as unhealthy for the request: it
// is not selected and it degrades its group, so a group whose only proxy died
// moves the route to another group instead of crossing LANs.
func TestPlanRouteUnreachableNodeDegradesItsGroup(t *testing.T) {
	deadProxyA := map[string]struct{}{"http://proxy-a": {}}
	f := newFixture(
		[]*Node{proxyNode(1, "http://proxy-a", strPtr("rack-a")), proxyNode(2, "http://proxy-b", strPtr("rack-b"))},
		[]*Node{
			transcodeNode(3, "http://tc-a", strPtr("rack-a"), 0),
			transcodeNode(4, "http://tc-b", strPtr("rack-b"), 0),
		},
	)
	plan := f.planner.PlanRoute(RouteRequest{
		SessionID: "s1", CurrentTranscodeURL: "http://tc-a",
		NeedsTranscode: true, NeedsProxy: true, Unreachable: deadProxyA,
	})
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-b" ||
		plan.ProxyNode == nil || plan.ProxyNode.URL != "http://proxy-b" {
		t.Fatalf("plan = %+v / %+v, want rack-b's tc-b and proxy-b", plan.TranscodeNode, plan.ProxyNode)
	}

	// With only rack-a and an ungrouped proxy, tc-a is never paired with the
	// ungrouped proxy while its own group's proxy is down.
	g := newFixture(
		[]*Node{proxyNode(1, "http://proxy-a", strPtr("rack-a")), proxyNode(2, "http://proxy-1", nil)},
		[]*Node{transcodeNode(3, "http://tc-a", strPtr("rack-a"), 0)},
	)
	plan = g.planner.PlanRoute(RouteRequest{
		SessionID: "s2", CurrentTranscodeURL: "http://tc-a",
		NeedsTranscode: true, NeedsProxy: true, Unreachable: deadProxyA,
	})
	if plan.TranscodeNode != nil || plan.ProxyNode != nil {
		t.Fatalf("plan = %+v / %+v, want no proxy route for rack-a without its proxy", plan.TranscodeNode, plan.ProxyNode)
	}

	// An unreachable transcode node degrades its group on the API-egress route too.
	h := newFixture(nil, []*Node{
		transcodeNode(3, "http://tc-a1", strPtr("rack-a"), 0),
		transcodeNode(4, "http://tc-a2", strPtr("rack-a"), 0),
		transcodeNode(5, "http://tc-1", nil, 3),
	})
	plan = h.planner.PlanRoute(RouteRequest{
		SessionID: "s3", CurrentTranscodeURL: "http://tc-a1",
		NeedsTranscode: true, Unreachable: map[string]struct{}{"http://tc-a1": {}},
	})
	if plan.TranscodeNode == nil || plan.TranscodeNode.URL != "http://tc-1" {
		t.Fatalf("plan = %+v, want ungrouped tc-1 while rack-a is degraded", plan.TranscodeNode)
	}
}

// newKeyedTranscodePool is a transcode pool in which every node has a key, so
// a fixture need not issue one before the node can take work.
func newKeyedTranscodePool() *TranscodePool {
	pool := NewTranscodePool()
	pool.SetKeyCheck(func(int) bool { return true })
	return pool
}

// A transcode node nothing can sign for stays pooled and listed but takes no
// work: not a session, not background work, and not a move off a dead node,
// even as the least loaded node. Without a key check the pool falls back to
// the row's KeyIssued.
func TestPlannerSkipsTranscodeNodesWithoutAKey(t *testing.T) {
	keyless := transcodeNode(1, "http://keyless", nil, 0)
	keyed := transcodeNode(2, "http://keyed", nil, 3)
	f := newFixture(nil, []*Node{keyless, keyed})
	f.transcodes.SetKeyCheck(func(id int) bool { return id == keyed.ID })

	if plan := f.planner.PlanRoute(RouteRequest{SessionID: "s1", NeedsTranscode: true}); plan.TranscodeNode == nil || plan.TranscodeNode.ID != keyed.ID {
		t.Fatalf("PlanRoute picked %+v, want the keyed node", plan.TranscodeNode)
	}
	// A move off the keyed node, reported unreachable, has nowhere to go.
	if plan := f.planner.PlanRoute(RouteRequest{SessionID: "s2", NeedsTranscode: true, Unreachable: map[string]struct{}{keyed.URL: {}}}); plan.TranscodeNode != nil {
		t.Fatalf("a move off the keyed node landed on %s", plan.TranscodeNode.URL)
	}
	if node, release := f.planner.ReserveTranscodeWork("job"); node == nil || node.ID != keyed.ID {
		t.Fatalf("ReserveTranscodeWork = %+v, want the keyed node", node)
	} else {
		release()
	}
	if f.planner.TranscodeNodeHealthy(keyless.URL) {
		t.Fatal("a keyless node reports as serving")
	}
	if got := f.planner.TranscodeNodeURLsForNewWork(); len(got) != 1 || got[0] != keyed.URL {
		t.Fatalf("TranscodeNodeURLsForNewWork = %v, want only the keyed node", got)
	}
	if got := f.transcodes.Acquire(); got == nil || got.ID != keyed.ID {
		t.Fatalf("Acquire = %+v, want the keyed node", got)
	}
	if len(f.transcodes.Nodes()) != 2 {
		t.Fatal("the keyless node left the pool")
	}

	unchecked := NewTranscodePool()
	unchecked.SetNodes([]*Node{transcodeNode(3, "http://unissued", nil, 0)})
	if unchecked.Acquire() != nil {
		t.Fatal("a pool without a key check placed work on a node with no issued key")
	}
}

// A node that refused new work at its own job cap takes none from this
// process until a health check newer than the refusal, or the hold's end,
// even while the row's cap says it has room.
func TestNodeAtCapacityIsHeldUntilItsNextHealthCheck(t *testing.T) {
	transcodes := newKeyedTranscodePool()
	transcodes.SetNodes([]*Node{{ID: 1, URL: "http://transcode-a", Enabled: true, Healthy: true}})
	planner := NewPlanner(NewProxyPool(), transcodes)
	now := time.Unix(1_000, 0)
	planner.now = func() time.Time { return now }

	ReportAtCapacity(planner, "http://transcode-a/")
	if planner.TranscodeWorkAvailableWith(nil) {
		t.Fatal("a node that refused at its cap still takes work")
	}
	transcodes.ApplyHealth(1, "http://transcode-a", true, 1, 0, "", nil, nil, now.Add(time.Second))
	if !planner.TranscodeWorkAvailableWith(nil) {
		t.Fatal("a health check after the refusal did not lift the hold")
	}
	// A refusal newer than the last health check holds the node again, and
	// then only time lifts it.
	now = now.Add(2 * time.Second)
	ReportAtCapacity(planner, "http://transcode-a")
	if planner.TranscodeWorkAvailableWith(nil) {
		t.Fatal("a refusal after the last health check did not hold the node")
	}
	now = now.Add(atCapacityHold)
	if !planner.TranscodeWorkAvailableWith(nil) {
		t.Fatal("the hold outlasted atCapacityHold")
	}
}

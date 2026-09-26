package noderouting

import (
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

func TestResolveFallsBackAcrossSoftPreference(t *testing.T) {
	decision, err := Resolve(nil, ResolveRequest{
		Request: Request{
			Workload: WorkloadVideoTranscode, Delivery: DeliveryHLSVideo,
			Policy: config.DefaultPlaybackRoutingPolicy(), ProxyAllowed: true,
		},
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Selected() || decision.Shape.ID != "hls_video_api" {
		t.Fatalf("decision = %#v, want local soft fallback", decision)
	}
}

func TestResolveDoesNotCrossHardExecutionConstraint(t *testing.T) {
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	decision, err := Resolve(nil, ResolveRequest{
		Request: Request{
			Workload: WorkloadVideoTranscode, Delivery: DeliveryHLSVideo,
			Policy: policy, ProxyAllowed: true,
		},
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != OutcomeCapacityUnavailable {
		t.Fatalf("decision = %#v, want capacity unavailable without a worker", decision)
	}
}

func TestResolveUsesExactWorkerAPIRoute(t *testing.T) {
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{ID: 1, URL: "http://worker", Enabled: true, Healthy: true}})
	planner := nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.VideoTranscodeEgress = config.PlaybackEgressAPIOnly
	decision, err := Resolve(planner, ResolveRequest{
		Request: Request{
			Workload: WorkloadVideoTranscode, Delivery: DeliveryHLSVideo,
			Policy: policy, ProxyAllowed: true,
		},
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Shape.ID != "hls_video_transcode_api" || decision.Plan.TranscodeNode == nil || decision.Plan.ProxyNode != nil {
		t.Fatalf("decision = %#v, want worker execution with API egress", decision)
	}
}

func TestResolveUsesTranscodeExecutorWithRelayOnlyProxy(t *testing.T) {
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{ID: 2, URL: "http://proxy", Enabled: true, Healthy: true}})
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{ID: 1, URL: "http://transcode", Enabled: true, Healthy: true}})
	planner := nodepool.NewPlanner(proxies, transcodes)
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode

	decision, err := Resolve(planner, ResolveRequest{
		Request:                Request{Workload: WorkloadRemux, Delivery: DeliveryProgressiveRemux, Policy: policy, ProxyAllowed: true},
		SessionID:              "session-1",
		TranscodeEligible:      func(node *nodepool.Node) bool { return node.URL == "http://transcode" },
		ProxyExecutionEligible: func(*nodepool.Node) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Shape.ID != "progressive_remux_transcode_proxy" ||
		decision.Plan.TranscodeNode == nil || decision.Plan.ProxyNode == nil {
		t.Fatalf("decision = %#v, want transcode execution with proxy egress", decision)
	}
}

func TestResolveProxyExecutorDoesNotRequireRelayCapability(t *testing.T) {
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{ID: 2, URL: "http://proxy", Enabled: true, Healthy: true}})
	planner := nodepool.NewPlanner(proxies, nodepool.NewTranscodePool())
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionWorkerOnly

	decision, err := Resolve(planner, ResolveRequest{
		Request:           Request{Workload: WorkloadRemux, Delivery: DeliveryProgressiveRemux, Policy: policy, ProxyAllowed: true},
		SessionID:         "session-1",
		ProxyEligible:     func(*nodepool.Node) bool { return false },
		TranscodeEligible: func(*nodepool.Node) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Shape.ID != "progressive_remux_proxy" || decision.Plan.ProxyNode == nil {
		t.Fatalf("decision = %#v, want proxy-executed remux", decision)
	}
}

type reservingLegacyPlanner struct {
	plan          nodepool.Plan
	released      []string
	releasedProxy []string
}

func (p *reservingLegacyPlanner) PlanSession(string, string, bool, int) nodepool.Plan {
	return p.plan
}

func (p *reservingLegacyPlanner) ReleaseSession(sessionID string) {
	p.released = append(p.released, sessionID)
}

func (p *reservingLegacyPlanner) ReleaseSessionProxy(sessionID string) {
	p.releasedProxy = append(p.releasedProxy, sessionID)
}

func TestSessionPlannerAdapterReleasesDiscardedProxyReservation(t *testing.T) {
	planner := &reservingLegacyPlanner{plan: nodepool.Plan{
		TranscodeNode: &nodepool.Node{URL: "http://worker"},
		ProxyNode:     &nodepool.Node{URL: "http://proxy"},
	}}

	plan := AdaptSessionPlanner(planner).PlanRoute(nodepool.RouteRequest{
		SessionID: "session-1", NeedsTranscode: true,
	})

	if plan.TranscodeNode == nil || plan.ProxyNode != nil {
		t.Fatalf("plan = %#v, want worker with local egress", plan)
	}
	if len(planner.releasedProxy) != 1 || planner.releasedProxy[0] != "session-1" {
		t.Fatalf("released proxy reservations = %v, want session-1", planner.releasedProxy)
	}
	if len(planner.released) != 0 {
		t.Fatalf("released full reservations = %v, want worker reservation retained", planner.released)
	}
}

func TestSessionPlannerAdapterReleasesRejectedPlan(t *testing.T) {
	planner := &reservingLegacyPlanner{plan: nodepool.Plan{
		TranscodeNode: &nodepool.Node{URL: "http://excluded-worker"},
		ProxyNode:     &nodepool.Node{URL: "http://proxy"},
	}}

	plan := AdaptSessionPlanner(planner).PlanRoute(nodepool.RouteRequest{
		SessionID: "session-1", NeedsTranscode: true, NeedsProxy: true,
		TranscodeEligible: func(*nodepool.Node) bool { return false },
	})

	if plan.TranscodeNode != nil || plan.ProxyNode != nil {
		t.Fatalf("plan = %#v, want rejected plan", plan)
	}
	if len(planner.released) != 1 || planner.released[0] != "session-1" {
		t.Fatalf("released reservations = %v, want session-1", planner.released)
	}
}

func TestResolveReleasesPreviousReservationForNodeFreeRoute(t *testing.T) {
	planner := &reservingLegacyPlanner{}
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.VideoTranscodeExecution = config.PlaybackExecutionAPIOnly
	policy.VideoTranscodeEgress = config.PlaybackEgressAPIOnly

	decision, err := Resolve(AdaptSessionPlanner(planner), ResolveRequest{
		Request: Request{
			Workload: WorkloadVideoTranscode, Delivery: DeliveryHLSVideo,
			Policy: policy, ProxyAllowed: true,
		},
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Selected() || decision.Shape.ID != "hls_video_api" {
		t.Fatalf("decision = %#v, want local API route", decision)
	}
	if len(planner.released) != 1 || planner.released[0] != "session-1" {
		t.Fatalf("released reservations = %v, want session-1", planner.released)
	}
}

func TestResolveCountsLowCardinalityDecisionMetric(t *testing.T) {
	counter := routingDecisions.WithLabelValues(
		string(WorkloadDirectPlay), string(ExecutionNone), string(EgressAPI),
		string(OutcomeSelected), "selected",
	)
	before := routingCounterValue(t, counter)

	decision, err := Resolve(nil, ResolveRequest{Request: Request{
		Workload: WorkloadDirectPlay, Delivery: DeliveryDirect,
		Policy: config.PlaybackRoutingPolicy{
			DirectPlayEgress: config.PlaybackEgressAPIOnly,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Selected() {
		t.Fatalf("decision = %#v, want selected", decision)
	}
	if got := routingCounterValue(t, counter); got != before+1 {
		t.Fatalf("decision counter = %v, want %v", got, before+1)
	}
}

func TestDecisionReasonUsesPolicyOutcomeWhenRejectionsAreMixed(t *testing.T) {
	decision := Decision{
		Outcome: OutcomePolicyUnsatisfied,
		Rejected: []Rejection{
			{ShapeID: "direct_proxy", Reason: RejectionClientUnsupported},
			{ShapeID: "direct_api", Reason: RejectionPolicyUnsatisfied},
		},
	}
	if got := decisionReason(decision); got != string(OutcomePolicyUnsatisfied) {
		t.Fatalf("decisionReason() = %q, want %q", got, OutcomePolicyUnsatisfied)
	}
}

func routingCounterValue(t *testing.T, counter interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

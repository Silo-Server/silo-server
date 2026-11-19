package noderouting

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

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

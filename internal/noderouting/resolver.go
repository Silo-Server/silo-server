package noderouting

import (
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type eligibleSessionPlanner interface {
	PlanSessionWith(sessionID, currentTranscodeURL string, needsTranscode bool, estimatedBitrateKbps int, eligible func(*nodepool.Node) bool) nodepool.Plan
}

type localEgressSessionPlanner interface {
	PlanTranscodeSessionWithLocalEgress(sessionID, currentTranscodeURL string, eligible func(*nodepool.Node) bool) nodepool.Plan
}

type sessionReservationReleaser interface {
	ReleaseSession(sessionID string)
}

type sessionProxyReservationReleaser interface {
	ReleaseSessionProxy(sessionID string)
}

type sessionPlannerAdapter struct {
	planner nodepool.SessionPlanner
}

// AdaptSessionPlanner preserves compatibility with small embedded/test planner
// implementations while production *nodepool.Planner uses the exact route
// contract. Protocol handlers still resolve every playback route here.
func AdaptSessionPlanner(planner nodepool.SessionPlanner) nodepool.RoutePlanner {
	if planner == nil {
		return nil
	}
	if routePlanner, ok := planner.(nodepool.RoutePlanner); ok {
		return routePlanner
	}
	return sessionPlannerAdapter{planner: planner}
}

func (a sessionPlannerAdapter) PlanRoute(request nodepool.RouteRequest) nodepool.Plan {
	var plan nodepool.Plan
	switch {
	case request.NeedsTranscode && !request.NeedsProxy:
		if planner, ok := a.planner.(localEgressSessionPlanner); ok {
			plan = planner.PlanTranscodeSessionWithLocalEgress(request.SessionID, request.CurrentTranscodeURL, request.TranscodeEligible)
			break
		}
		plan = a.planSession(request, true, request.TranscodeEligible)
		if plan.ProxyNode != nil {
			// Legacy transcode plans may reserve both halves. A local-egress
			// route keeps the worker but must return the unused proxy capacity.
			if releaser, ok := a.planner.(sessionProxyReservationReleaser); ok {
				releaser.ReleaseSessionProxy(request.SessionID)
			} else if releaser, ok := a.planner.(sessionReservationReleaser); ok {
				// Without a half-release contract the transcode reservation cannot
				// be kept without leaking the proxy half. Release both and let the
				// resolver try another legal shape.
				releaser.ReleaseSession(request.SessionID)
				return nodepool.Plan{}
			}
			plan.ProxyNode = nil
		}
	case request.NeedsTranscode:
		plan = a.planSession(request, true, request.TranscodeEligible)
	case request.NeedsProxy:
		plan = a.planSession(request, false, request.ProxyEligible)
	default:
		return nodepool.Plan{}
	}
	// A minimal legacy planner may not accept eligibility predicates. Keep that
	// compatibility seam from returning a node the shared resolver explicitly
	// excluded; production *nodepool.Planner evaluates predicates before it
	// reserves, so this guard is only a defensive adapter boundary.
	if plan.TranscodeNode != nil && request.TranscodeEligible != nil && !request.TranscodeEligible(plan.TranscodeNode) {
		a.releaseSession(request.SessionID)
		return nodepool.Plan{}
	}
	if plan.ProxyNode != nil && request.ProxyEligible != nil && !request.ProxyEligible(plan.ProxyNode) {
		a.releaseSession(request.SessionID)
		return nodepool.Plan{}
	}
	if request.NeedsTranscode != (plan.TranscodeNode != nil) || request.NeedsProxy != (plan.ProxyNode != nil) {
		if plan.TranscodeNode != nil || plan.ProxyNode != nil {
			a.releaseSession(request.SessionID)
		}
		return nodepool.Plan{}
	}
	return plan
}

func (a sessionPlannerAdapter) releaseSession(sessionID string) {
	if releaser, ok := a.planner.(sessionReservationReleaser); ok {
		releaser.ReleaseSession(sessionID)
	}
}

// ReleaseSession lets Resolve reconcile an earlier reservation even when its
// caller supplied the legacy planner contract through AdaptSessionPlanner.
func (a sessionPlannerAdapter) ReleaseSession(sessionID string) {
	a.releaseSession(sessionID)
}

func (a sessionPlannerAdapter) planSession(request nodepool.RouteRequest, transcode bool, eligible func(*nodepool.Node) bool) nodepool.Plan {
	if planner, ok := a.planner.(eligibleSessionPlanner); ok && eligible != nil {
		return planner.PlanSessionWith(request.SessionID, request.CurrentTranscodeURL, transcode,
			request.EstimatedBitrateKbps, eligible)
	}
	return a.planner.PlanSession(request.SessionID, request.CurrentTranscodeURL, transcode, request.EstimatedBitrateKbps)
}

type ResolveRequest struct {
	Request
	SessionID              string
	CurrentTranscodeURL    string
	EstimatedBitrateKbps   int
	TranscodeEligible      func(*nodepool.Node) bool
	ProxyEligible          func(*nodepool.Node) bool
	ProxyExecutionEligible func(*nodepool.Node) bool
	ExcludedShapeIDs       map[string]struct{}
}

type Outcome string

const (
	OutcomeSelected            Outcome = "selected"
	OutcomePolicyUnsatisfied   Outcome = "routing_policy_unsatisfied"
	OutcomeCapacityUnavailable Outcome = "route_capacity_unavailable"
)

type Decision struct {
	Shape    Shape
	Plan     nodepool.Plan
	Outcome  Outcome
	Rejected []Rejection
}

func (d Decision) Selected() bool { return d.Outcome == OutcomeSelected }

// Resolve compiles policy and asks nodepool for each legal route in order.
// The first route whose exact node requirements can be reserved wins. Local
// routes need no planner and are therefore the natural single-node fallback.
func Resolve(planner nodepool.RoutePlanner, request ResolveRequest) (Decision, error) {
	compiled, err := Candidates(request.Request)
	if err != nil {
		return Decision{}, err
	}
	if len(compiled.Candidates) == 0 {
		return decided(request.Workload, Decision{Outcome: OutcomePolicyUnsatisfied, Rejected: compiled.Rejected})
	}

	plannerAttempted := false
	for _, shape := range compiled.Candidates {
		if _, excluded := request.ExcludedShapeIDs[shape.ID]; excluded {
			continue
		}
		if !shape.NeedsTranscodeNode() && !shape.NeedsProxyNode() {
			// Exact route planning normally replaces this session's provisional
			// reservation. A node-free route bypasses the planner, so explicitly
			// clear any reservation left by the session's previous route.
			if !plannerAttempted {
				if releaser, ok := planner.(sessionReservationReleaser); ok {
					releaser.ReleaseSession(request.SessionID)
				}
			}
			return decided(request.Workload, Decision{Shape: shape, Outcome: OutcomeSelected, Rejected: compiled.Rejected})
		}
		if planner == nil {
			continue
		}
		plannerAttempted = true
		proxyEligible := request.ProxyEligible
		if shape.Execution == ExecutionProxy {
			proxyEligible = request.ProxyExecutionEligible
		}
		plan := planner.PlanRoute(nodepool.RouteRequest{
			SessionID:            request.SessionID,
			CurrentTranscodeURL:  request.CurrentTranscodeURL,
			EstimatedBitrateKbps: request.EstimatedBitrateKbps,
			NeedsTranscode:       shape.NeedsTranscodeNode(),
			NeedsProxy:           shape.NeedsProxyNode(),
			TranscodeEligible:    request.TranscodeEligible,
			ProxyEligible:        proxyEligible,
		})
		if shape.NeedsTranscodeNode() != (plan.TranscodeNode != nil) ||
			shape.NeedsProxyNode() != (plan.ProxyNode != nil) {
			continue
		}
		return decided(request.Workload, Decision{Shape: shape, Plan: plan, Outcome: OutcomeSelected, Rejected: compiled.Rejected})
	}

	return decided(request.Workload, Decision{Outcome: OutcomeCapacityUnavailable, Rejected: compiled.Rejected})
}

// decided observes and returns a finished routing decision. Every exit from
// Resolve goes through it so the metric and the log can never disagree about
// what was chosen.
func decided(workload Workload, decision Decision) (Decision, error) {
	logDecision(workload, decision)
	return decision, nil
}

func logDecision(workload Workload, decision Decision) {
	observeDecision(workload, decision)
	if decision.Selected() {
		slog.Debug("playback route selected", "component", "noderouting",
			"workload", decision.Shape.Workload, "shape", decision.Shape.ID,
			"execution", decision.Shape.Execution, "egress", decision.Shape.Egress)
		return
	}
	slog.Warn("playback route unavailable", "component", "noderouting", "workload", workload,
		"outcome", decision.Outcome, "reason", decisionReason(decision))
}

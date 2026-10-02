package noderouting

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var routingDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_playback_routing_decisions_total",
	Help: "Playback route decisions by workload, selected topology, and outcome.",
}, []string{"workload", "execution", "egress", "outcome", "reason"})

func observeDecision(workload Workload, decision Decision) {
	execution := "none"
	egress := "none"
	reason := decisionReason(decision)
	if decision.Selected() {
		execution = string(decision.Shape.Execution)
		egress = string(decision.Shape.Egress)
	}
	routingDecisions.WithLabelValues(
		string(workload), execution, egress, string(decision.Outcome), reason,
	).Inc()
}

func decisionReason(decision Decision) string {
	if decision.Selected() {
		return "selected"
	}
	clientUnsupported := len(decision.Rejected) > 0
	for _, rejection := range decision.Rejected {
		if rejection.Reason != RejectionClientUnsupported {
			clientUnsupported = false
			break
		}
	}
	if clientUnsupported {
		return string(RejectionClientUnsupported)
	}
	return string(decision.Outcome)
}

package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type PolicyCapabilityService interface {
	PolicyCapability(context.Context) handlers.PolicyCapabilityView
}
type PolicyCapability struct {
	Capability
	Enabled         bool     `json:"enabled"`
	EditorAvailable bool     `json:"editor_available"`
	DecisionTypes   []string `json:"decision_types"`
	Generation      int64    `json:"generation"`
	Degraded        bool     `json:"degraded"`
	DegradedReason  string   `json:"degraded_reason,omitempty"`
	DegradedDomains []string `json:"degraded_domains,omitempty"`
	EvalTimeouts    int64    `json:"eval_timeouts"`
}
type PolicyCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         PolicyCapability
}

func registerPolicyCapability(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/policy/capability", "getPolicyCapability", "policy", "Discover policy availability and runtime health."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}, func(ctx context.Context, _ *CapabilityInput) (*PolicyCapabilityOutput, error) {
		view := handlers.PolicyCapabilityView{DecisionTypes: policy.DecisionTypes()}
		if reg.deps.PolicyCapability != nil {
			view = reg.deps.PolicyCapability.PolicyCapability(ctx)
		}
		if view.DecisionTypes == nil {
			view.DecisionTypes = []string{}
		}
		return &PolicyCapabilityOutput{Body: PolicyCapability{Capability: Capability{State: configuredEnabledCapabilityState(reg.deps.PolicyCapability != nil, view.Enabled)}, Enabled: view.Enabled, EditorAvailable: view.EditorAvailable, DecisionTypes: view.DecisionTypes, Generation: view.Generation, Degraded: view.Degraded, DegradedReason: view.DegradedReason, DegradedDomains: view.DegradedDomains, EvalTimeouts: view.EvalTimeouts}}, nil
	})
}

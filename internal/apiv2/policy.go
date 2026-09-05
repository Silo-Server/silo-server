package apiv2

import (
	"context"
	"net/http"
)

// The policy domain's capability document: whether the policy engine is
// running, whether the editor is available, and its health. Profile scoped
// as on v1 (the document is answered for a signed-in viewer); the engine's
// answer does not vary per profile.

// PolicyCapability is the policy-engine capability document.
type PolicyCapability struct {
	Capability
	EditorAvailable bool     `json:"editor_available" doc:"Whether the policy editor is enabled for administrators" example:"false"`
	DecisionTypes   []string `json:"decision_types" doc:"The policy domains the engine decides" example:"[\"playback\",\"download\"]"`
	Generation      int64    `json:"generation" doc:"Monotonic generation of the loaded policy bundle" example:"3"`
	Degraded        bool     `json:"degraded" doc:"True when the engine runs without part of its configured policy" example:"false"`
	DegradedReason  string   `json:"degraded_reason,omitempty" doc:"Why the engine is degraded; absent when it is not" example:"policy store unavailable"`
	DegradedDomains []string `json:"degraded_domains" doc:"Domains whose custom policy was dropped; empty when none" example:"[]"`
	EvalTimeouts    int64    `json:"eval_timeouts" doc:"Evaluations that hit the time budget since start" example:"0"`
}

// PolicyCapabilityOutput is the getPolicyCapability response.
type PolicyCapabilityOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         PolicyCapability
}

func registerPolicy(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/policy/capability", "getPolicyCapability", "policy",
			"Describe the policy engine."),
		Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true,
	}, reg.getPolicyCapability)
}

// getPolicyCapability answers from the same engine v1 GET /policy/capability
// reads. An unconfigured engine is state not_configured rather than v1's
// 503: the route is always served and reports state.
func (reg *Registry) getPolicyCapability(ctx context.Context, _ *struct{}) (*PolicyCapabilityOutput, error) {
	if claimsFrom(ctx) == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	out := &PolicyCapabilityOutput{CacheControl: cacheNoCache, Body: PolicyCapability{
		Capability:      Capability{Revision: "1", State: StateNotConfigured},
		DecisionTypes:   []string{},
		DegradedDomains: []string{},
	}}
	if reg.deps.Policy == nil {
		return out, nil
	}
	view, ok := reg.deps.Policy.Capability()
	if !ok {
		return out, nil
	}
	out.Body.State = StateAvailable
	out.Body.EditorAvailable = view.EditorAvailable
	out.Body.DecisionTypes = NonNil(view.DecisionTypes)
	out.Body.Generation = view.Generation
	out.Body.Degraded = view.Degraded
	out.Body.DegradedReason = view.DegradedReason
	out.Body.DegradedDomains = NonNil(view.DegradedDomains)
	out.Body.EvalTimeouts = view.EvalTimeouts
	return out, nil
}

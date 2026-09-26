package apiv2

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakePolicyCapability struct{ calls int }

func (f *fakePolicyCapability) PolicyCapability(context.Context) handlers.PolicyCapabilityView {
	f.calls++
	return handlers.PolicyCapabilityView{Enabled: true, EditorAvailable: false, DecisionTypes: []string{"permission"}, Generation: 7, Degraded: true, DegradedReason: "reload failed", DegradedDomains: []string{"permission"}, EvalTimeouts: 3}
}
func TestPolicyCapabilityDiscovery(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakePolicyCapability)
	deps.PolicyCapability = f
	h := NewHandler(deps)
	// Discovery remains available to an ordinary account before profile selection,
	// even when policy editing is disabled.
	got := do(t, h, "GET", Prefix+"/policy/capability", "", bearer(memberToken))
	var body PolicyCapability
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got.Code != 200 || !body.Enabled || body.EditorAvailable || body.Generation != 7 || !body.Degraded || body.EvalTimeouts != 3 {
		t.Fatal(got.Code, got.Body.String())
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/policy/capability", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", Prefix+"/policy/capability", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	if f.calls != 1 {
		t.Fatal("authorization failure reached service")
	}
	deps.PolicyCapability = nil
	got = do(t, NewHandler(deps), "GET", Prefix+"/policy/capability", "", bearer(memberToken))
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got.Code != 200 || body.Enabled || body.EditorAvailable || len(body.DecisionTypes) == 0 {
		t.Fatal(got.Code, got.Body.String())
	}
}

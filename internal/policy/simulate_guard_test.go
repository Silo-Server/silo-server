package policy

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestGuardEvalCostRejectsSlowOverride pins the C7 activation guard: a custom
// override that cannot evaluate within the budget is rejected with
// ErrPolicySlowEval instead of activating and converting to request-path
// failures for every viewer.
func TestGuardEvalCostRejectsSlowOverride(t *testing.T) {
	slow := `package silo_custom.scope

import rego.v1

override(base, _) := base if {
	count([x |
		some i in numbers.range(1, 10000)
		some j in numbers.range(1, 10000)
		x := i + j
	]) > 0
}`
	err := GuardEvalCost(context.Background(), DomainScope, slow, time.Millisecond)
	if !errors.Is(err, ErrPolicySlowEval) {
		t.Fatalf("GuardEvalCost(slow) error = %v, want ErrPolicySlowEval", err)
	}
}

// TestGuardEvalCostAcceptsCheapOverridesForAllDomains verifies the canned
// benchmark inputs evaluate cleanly (defined decisions) with a trivial
// tightening override in every domain, so the guard never false-positives on
// well-behaved policies.
func TestGuardEvalCostAcceptsCheapOverridesForAllDomains(t *testing.T) {
	sources := map[string]string{
		DomainScope: tighteningScopeOverrideSource(),
		DomainPermission: `package silo_custom.permission

import rego.v1

override(base, _) := base`,
		DomainAction: `package silo_custom.action

import rego.v1

override(base, _) := base`,
	}
	for domain, source := range sources {
		if err := GuardEvalCost(context.Background(), domain, source, time.Second); err != nil {
			t.Fatalf("GuardEvalCost(%s) error = %v, want nil", domain, err)
		}
	}
}

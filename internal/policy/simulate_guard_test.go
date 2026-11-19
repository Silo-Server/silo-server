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

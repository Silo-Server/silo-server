package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-policy-agent/opa/v1/rego"
)

func TestLockedCapabilitiesRejectHTTP(t *testing.T) {
	source := `package silo_custom.scope

import rego.v1

override(base, _) := base if {
	http.send({"method": "get", "url": "https://example.test"})
}`
	_, err := rego.New(
		rego.Query("data.silo_custom.scope.override({}, {})"),
		rego.Module("bad.rego", source),
		rego.Capabilities(LockedCapabilities()),
	).PrepareForEval(context.Background())
	if err == nil {
		t.Fatal("expected http.send compile failure")
	}
}

func TestEvaluateTimeoutFailsClosed(t *testing.T) {
	engine, err := newEngineFromModules(context.Background(), time.Nanosecond, []ModuleSource{{
		Path: "slow.rego",
		Source: `package silo.slow

import rego.v1

decision := count([x |
	some i in numbers.range(1, 10000000)
	some j in numbers.range(1, 10000000)
	x := i + j
])`,
	}}, map[DecisionName]string{
		"slow.decision": "data.silo.slow.decision",
	})
	if err != nil {
		t.Fatalf("compile slow policy: %v", err)
	}

	var out int
	_, err = engine.Evaluate(context.Background(), "slow.decision", map[string]any{}, &out)
	if !errors.Is(err, ErrPolicyEvalFailed) {
		t.Fatalf("Evaluate() error = %v, want ErrPolicyEvalFailed", err)
	}
}

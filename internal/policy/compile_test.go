package policy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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

func TestCompileCheckRejectsForbiddenBuiltins(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name: "http_send",
			source: `package silo_custom.scope

import rego.v1

override(base, _) := base if {
	resp := http.send({"method": "get", "url": "https://example.test"})
	resp.status_code >= 100
}`,
		},
		{
			name: "net_lookup",
			source: `package silo_custom.scope

import rego.v1

override(base, _) := base if {
	ips := net.lookup_ip_addr("localhost")
	count(ips) >= 0
}`,
		},
		{
			name: "opa_runtime",
			source: `package silo_custom.scope

import rego.v1

override(base, _) := base if {
	runtime := opa.runtime()
	object.get(runtime, "version", "") == ""
}`,
		},
		{
			name: "time_now",
			source: `package silo_custom.scope

import rego.v1

override(base, _) := base if {
	time.now_ns() > 0
}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CompileCheck(context.Background(), "scope", test.source)
			if !errors.Is(err, ErrCompileFailed) {
				t.Fatalf("CompileCheck() error = %v, want ErrCompileFailed", err)
			}
		})
	}
}

func TestCompileCheckRejectsWrongPackagePath(t *testing.T) {
	err := CompileCheck(context.Background(), "scope", `package silo_custom.permission

import rego.v1

override(base, _) := base`)
	if !errors.Is(err, ErrCompileFailed) {
		t.Fatalf("CompileCheck() error = %v, want ErrCompileFailed", err)
	}
	if !strings.Contains(err.Error(), "policy package must be silo_custom.scope") {
		t.Fatalf("CompileCheck() error = %v, want package mismatch", err)
	}
}

func TestCompileCheckAllowsPureNetHelpers(t *testing.T) {
	// net.cidr_contains is deterministic; only impure builtins are locked.
	err := CompileCheck(context.Background(), "scope", `package silo_custom.scope

import rego.v1

override(base, i) := base if {
	net.cidr_contains("10.0.0.0/8", object.get(i, "client_ip", "10.1.2.3"))
}`)
	if err != nil {
		t.Fatalf("CompileCheck() error = %v, want pure net helper allowed", err)
	}
}

func TestCompileCheckRejectsOversizedSource(t *testing.T) {
	source := "package silo_custom.scope\n\nimport rego.v1\n\n# " +
		strings.Repeat("x", maxPolicySourceBytes)
	err := CompileCheck(context.Background(), "scope", source)
	if !errors.Is(err, ErrCompileFailed) {
		t.Fatalf("CompileCheck() error = %v, want ErrCompileFailed", err)
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("CompileCheck() error = %v, want size limit message", err)
	}
}

func TestCompileCheckAcceptsValidScopeOverride(t *testing.T) {
	if err := CompileCheck(context.Background(), "scope", tighteningScopeOverrideSource()); err != nil {
		t.Fatalf("CompileCheck() error: %v", err)
	}
}

func TestCustomStubAndScopeOverrideCoexist(t *testing.T) {
	engine, err := NewEngineWithCustom(context.Background(), map[string]ActiveSource{
		"scope": {DocumentID: 1, VersionID: 1, Source: tighteningScopeOverrideSource()},
	})
	if err != nil {
		t.Fatalf("NewEngineWithCustom() error: %v", err)
	}
	pdp := NewPDP(engine)

	decision, _, err := pdp.ResolveViewerScope(context.Background(), ScopeInput{
		SchemaVersion:        1,
		UserID:               42,
		SessionID:            "sess-1",
		AccountRestricted:    false,
		AccessPolicyRevision: 9,
		ProfileVerified:      true,
		RequestTime:          "2026-07-02T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("ResolveViewerScope() error: %v", err)
	}
	if decision.Unrestricted {
		t.Fatalf("Unrestricted = true, want tightened restricted decision: %#v", decision)
	}
	if got, want := decision.AllowedLibraryIDs, []int{2}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("AllowedLibraryIDs = %#v, want %#v", got, want)
	}
}

func TestNewEngineWithCustomSkipsInvalidSource(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	engine, err := NewEngineWithCustom(
		context.Background(),
		map[string]ActiveSource{
			"scope": {
				DocumentID: 123,
				VersionID:  456,
				Source: `package silo_custom.scope

import rego.v1

override(base, _) := base if {`,
			},
		},
		WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("NewEngineWithCustom() error: %v", err)
	}
	pdp := NewPDP(engine)
	decision, _, err := pdp.ResolveViewerScope(context.Background(), ScopeInput{
		SchemaVersion:        1,
		UserID:               42,
		SessionID:            "sess-1",
		AccountRestricted:    false,
		AccessPolicyRevision: 9,
		ProfileVerified:      true,
		RequestTime:          "2026-07-02T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("ResolveViewerScope() error: %v", err)
	}
	if !decision.Unrestricted {
		t.Fatalf("Unrestricted = false, want vendor-only unrestricted decision: %#v", decision)
	}

	logOutput := logs.String()
	if !strings.Contains(logOutput, "skipping invalid custom policy source") ||
		!strings.Contains(logOutput, "domain=scope") ||
		!strings.Contains(logOutput, "document_id=123") {
		t.Fatalf("error log did not include skip fields: %s", logOutput)
	}

	skipped := engine.SkippedSources()
	if len(skipped) != 1 || skipped[0].Domain != "scope" || skipped[0].DocumentID != 123 || skipped[0].Err == nil {
		t.Fatalf("SkippedSources() = %#v, want the dropped scope source recorded", skipped)
	}
}

func TestEngineReloadRejectsInvalidCustomSource(t *testing.T) {
	ctx := context.Background()
	engine, err := NewEngineWithCustom(ctx, map[string]ActiveSource{
		"scope": {DocumentID: 1, VersionID: 1, Source: tighteningScopeOverrideSource()},
	}, WithRevision(7))
	if err != nil {
		t.Fatalf("NewEngineWithCustom() error: %v", err)
	}

	err = engine.Reload(ctx, map[string]ActiveSource{
		"scope": {
			DocumentID: 1,
			VersionID:  2,
			Source: `package silo_custom.scope

import rego.v1

override(base, _) := base if {`,
		},
	}, 8)
	if err == nil {
		t.Fatal("Reload() error = nil, want compile failure for the invalid custom source")
	}
	if !strings.Contains(err.Error(), `domain "scope"`) || !strings.Contains(err.Error(), "document 1") {
		t.Fatalf("Reload() error = %v, want domain and document identified", err)
	}

	// The last known-good bundle must keep serving: revision unchanged and the
	// previous tightening override still applied.
	if got := engine.Revision(); got != 7 {
		t.Fatalf("Revision() after failed reload = %d, want 7", got)
	}
	pdp := NewPDP(engine)
	decision, _, err := pdp.ResolveViewerScope(ctx, ScopeInput{
		SchemaVersion:        1,
		UserID:               42,
		SessionID:            "sess-1",
		AccountRestricted:    false,
		AccessPolicyRevision: 9,
		ProfileVerified:      true,
		RequestTime:          "2026-07-02T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("ResolveViewerScope() error: %v", err)
	}
	if decision.Unrestricted {
		t.Fatalf("Unrestricted = true after failed reload, want last known-good tightened decision: %#v", decision)
	}
}

func TestEvaluateTimeoutFailsClosed(t *testing.T) {
	engine, err := newEngineFromModules(context.Background(), time.Nanosecond, []ModuleSource{{
		Path: "slow.rego",
		Source: `package silo.slow

import rego.v1

decision := count([x |
	some i in numbers.range(1, 10000)
	some j in numbers.range(1, 10000)
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
	// Timeouts stay fail-closed but carry the distinct sentinel and counter so
	// a slow policy is attributable, not just a generic eval failure.
	if !errors.Is(err, ErrPolicyEvalTimeout) {
		t.Fatalf("Evaluate() error = %v, want ErrPolicyEvalTimeout to match", err)
	}
	if got := engine.EvalTimeouts(); got != 1 {
		t.Fatalf("EvalTimeouts() = %d, want 1", got)
	}
}

func tighteningScopeOverrideSource() string {
	return `package silo_custom.scope

import rego.v1

override(_, _) := result if {
	result := {
		"unrestricted": false,
		"allowed_library_ids": [2],
	}
}`
}

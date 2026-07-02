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
		t.Fatalf("warning log did not include skip fields: %s", logOutput)
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

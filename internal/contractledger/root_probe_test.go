package contractledger

import (
	"testing"
)

// probeRetained mutates the first contract_root_probes row on the given
// listener into the retained ratified shape: v2 unset and the retention note.
// Callers break exactly one of those to prove each rule.
func probeRetained(t *testing.T, doc map[string]any, listener string) map[string]any {
	t.Helper()
	e := entryWhere(t, doc, func(e map[string]any) bool {
		return e["listener"] == listener && e["disposition_rule"] == ruleContractRootProbes
	})
	e["review_state"] = ReviewRatified
	e["owner"] = "Quick104"
	e["v2"] = map[string]any{"method": nil, "path": nil, "operation_id": nil}
	e["notes"] = probeRetentionNotesPrefix + listener + " listener with public; no v2 JSON operation; test."
	return e
}

// TestRootProbeRetainedRowIsRatifiable proves the probe exception on every
// listener that serves a health or readiness probe: the row is ratified with
// v2 unset and the retention note, without the node listener_delegation rule.
func TestRootProbeRetainedRowIsRatifiable(t *testing.T) {
	for _, listener := range []string{ListenerAPI, ListenerProxy, ListenerTranscodeNode} {
		t.Run(listener, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) { probeRetained(t, doc, listener) })
			if err := verify(fsys); err != nil {
				t.Fatalf("retained %s probe refused: %v", listener, err)
			}
		})
	}
}

// TestRootProbeRatifiedRowNeedsUnsetV2AndNote breaks each half of the
// retained shape; the schema and the gate's review rule both refuse it.
func TestRootProbeRatifiedRowNeedsUnsetV2AndNote(t *testing.T) {
	cases := map[string]struct {
		mutate func(e map[string]any)
		want   string
	}{
		"partial v2 target kept": {func(e map[string]any) {
			e["v2"] = map[string]any{"method": "GET", "path": "/health", "operation_id": nil}
		}, "violates"},
		"full v2 target": {func(e map[string]any) {
			e["v2"] = map[string]any{"method": "GET", "path": "/api/v2/health", "operation_id": "getHealth"}
		}, "violates"},
		"missing note":        {func(e map[string]any) { e["notes"] = "Administrator action: rewrite probes." }, "violates"},
		"node retention note": {func(e map[string]any) { e["notes"] = "Retained on api listener with public." }, "violates"},
		"other listener note": {func(e map[string]any) { e["notes"] = "Retained as unversioned probe on proxy listener with public." }, "notes must open with"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) { c.mutate(probeRetained(t, doc, ListenerAPI)) })
			expectFailure(t, fsys, c.want)
		})
	}
}

// TestAPIPortedRowCannotBorrowTheProbeException pins the scope of the
// exception: an API-listener ported row ratified with v2 unset still fails,
// with or without the probe note, because contract_root_probes fits only a
// redesigned row and the ported rule still demands a complete v2 target.
func TestAPIPortedRowCannotBorrowTheProbeException(t *testing.T) {
	for name, mutate := range map[string]func(e map[string]any){
		"probe note only": func(e map[string]any) {
			e["notes"] = probeRetentionNotesPrefix + "api listener with public; not a probe."
		},
		"probe rule on a ported row": func(e map[string]any) {
			e["disposition_rule"] = ruleContractRootProbes
			e["notes"] = probeRetentionNotesPrefix + "api listener with public; not a probe."
		},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) {
				e := entryWhere(t, doc, func(e map[string]any) bool {
					return e["listener"] == ListenerAPI && e["disposition"] == DispositionPorted
				})
				e["review_state"] = ReviewRatified
				e["v2"] = map[string]any{"method": nil, "path": nil, "operation_id": nil}
				mutate(e)
			})
			expectFailure(t, fsys, "violates")
		})
	}
}

// TestRootProbeProposedRowKeepsPartialTarget keeps the pre-decision shape
// valid: a proposed probe row may still record its partial v2 target.
func TestRootProbeProposedRowKeepsPartialTarget(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range ledger.Entries {
		if e.DispositionRule == ruleContractRootProbes {
			seen++
			if e.ReviewState == ReviewRatified && (e.V2.Method != nil || e.V2.Path != nil) {
				t.Errorf("%s: ratified probe still names a v2 target", e.key())
			}
		}
	}
	if seen != 4 {
		t.Fatalf("contract_root_probes rows = %d, want 4", seen)
	}
}

package contractledger

import (
	"strings"
	"testing"
)

// nodeRetained mutates the first ported row on the given node listener into
// the retained ratified shape: v2 unset, listener_delegation and the
// retention note. Callers then break exactly one of those to prove each rule.
func nodeRetained(t *testing.T, doc map[string]any, listener string) map[string]any {
	t.Helper()
	e := entryWhere(t, doc, func(e map[string]any) bool {
		return e["listener"] == listener && e["disposition"] == DispositionPorted
	})
	e["review_state"] = ReviewRatified
	e["disposition_rule"] = ruleListenerDelegation
	e["v2"] = map[string]any{"method": nil, "path": nil, "operation_id": nil}
	e["notes"] = "Retained on " + listener + " listener with " + e["auth_class"].(string) + "; v2 unset by design; description: x-silo-worker-protocols test."
	return e
}

// TestNodeListenerRetainedRowIsRatifiable proves the listener-scoped
// exception: a proxy or transcode_node ported row may be ratified with v2
// unset when it carries the listener_delegation rule and the retention note.
func TestNodeListenerRetainedRowIsRatifiable(t *testing.T) {
	for _, listener := range []string{ListenerProxy, ListenerTranscodeNode} {
		t.Run(listener, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) { nodeRetained(t, doc, listener) })
			if err := verify(fsys); err != nil {
				t.Fatalf("retained %s row refused: %v", listener, err)
			}
		})
	}
}

// TestAPIListenerRatifiedPortStillNeedsV2Target pins that the exception is
// scoped to the node listeners: an API-listener ported row ratified with v2
// unset still violates the schema, with or without the node-only rule.
func TestAPIListenerRatifiedPortStillNeedsV2Target(t *testing.T) {
	for name, rule := range map[string]string{"default rule": "default_ported", "node rule": ruleListenerDelegation} {
		t.Run(name, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) {
				e := entryWhere(t, doc, func(e map[string]any) bool {
					return e["listener"] == ListenerAPI && e["disposition"] == DispositionPorted
				})
				e["review_state"] = ReviewRatified
				e["disposition_rule"] = rule
				e["v2"] = map[string]any{"method": nil, "path": nil, "operation_id": nil}
				e["notes"] = "Retained on api listener with public; not a valid shape."
			})
			expectFailure(t, fsys, "violates")
		})
	}
}

// TestNodeListenerRatifiedRowNeedsRuleAndNote breaks each half of the
// retained shape in turn. Both the schema and the gate's review rule refuse
// it; the assertion names the gate message so the failure is legible.
func TestNodeListenerRatifiedRowNeedsRuleAndNote(t *testing.T) {
	cases := map[string]struct {
		mutate func(e map[string]any)
		want   string
	}{
		"default_ported rule": {func(e map[string]any) { e["disposition_rule"] = "default_ported" }, "violates"},
		"maintainer rule":     {func(e map[string]any) { e["disposition_rule"] = "maintainer_decision" }, "violates"},
		"missing note":        {func(e map[string]any) { e["notes"] = "v2 is null by design for node-listener rows." }, "violates"},
		"other listener note": {func(e map[string]any) { e["notes"] = "Retained on transcode_node listener with node_bearer." }, "notes must open with"},
		"v2 target set": {func(e map[string]any) {
			e["v2"] = map[string]any{"method": "GET", "path": "/api/v2/x", "operation_id": "getX"}
		}, "violates"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) { c.mutate(nodeRetained(t, doc, ListenerProxy)) })
			expectFailure(t, fsys, c.want)
		})
	}
}

// TestNodeListenerProposedRowIsUnconstrained keeps the pre-decision shape
// valid: a proposed node row with default_ported and the historical note.
func TestNodeListenerProposedRowIsUnconstrained(t *testing.T) {
	ledger, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range ledger.Entries {
		if isNodeListener(e.Listener) && e.Disposition == DispositionPorted && e.ReviewState == ReviewProposed {
			seen++
			if strings.HasPrefix(e.Notes, nodeListenerRetentionPrefix) {
				t.Errorf("%s: proposed row already claims retention", e.key())
			}
		}
	}
	t.Logf("proposed node-listener ported rows: %d", seen)
}

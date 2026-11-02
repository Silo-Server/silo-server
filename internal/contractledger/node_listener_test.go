package contractledger

import (
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

// TestNodeListenerRatifiedRowNeedsRuleAndNote breaks each half of the
// retained shape in turn. Both the schema and the gate's review rule refuse
// it; the assertion names the gate message so the failure is legible.
func TestNodeListenerRatifiedRowNeedsRuleAndNote(t *testing.T) {
	cases := map[string]struct {
		mutate func(e map[string]any)
		want   string
	}{
		"default_ported rule": {func(e map[string]any) { e["disposition_rule"] = "default_ported" }, "violates"},
		"other listener note": {func(e map[string]any) { e["notes"] = "Retained on transcode_node listener with node_bearer." }, "notes must open with"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := mutatedFS(t, func(doc map[string]any) { c.mutate(nodeRetained(t, doc, ListenerProxy)) })
			expectFailure(t, fsys, c.want)
		})
	}
}

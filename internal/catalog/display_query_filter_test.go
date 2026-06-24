package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeDisplayQueryFragment_EmptyInputs(t *testing.T) {
	for _, raw := range []string{"", "   ", "null", `{"match":"all","groups":[]}`, `{"match":"all","groups":[{"match":"all","rules":[]}]}`} {
		got, err := NormalizeDisplayQueryFragment([]byte(raw))
		if err != nil {
			t.Fatalf("NormalizeDisplayQueryFragment(%q) returned error: %v", raw, err)
		}
		if got != "" {
			t.Fatalf("NormalizeDisplayQueryFragment(%q) = %q, want empty", raw, got)
		}
	}
}

func TestNormalizeDisplayQueryFragment_CanonicalizesTypeAndWatched(t *testing.T) {
	raw := `{"match":"all","groups":[{"match":"all","rules":[
		{"field":"watched","op":"is","value":true},
		{"field":"type","op":"is","value":"movie"}
	]}]}`
	got, err := NormalizeDisplayQueryFragment([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeDisplayQueryFragment returned error: %v", err)
	}

	// Canonical output is filter-only: no sort/limit/library_ids/media_scope.
	for _, banned := range []string{"sort", "limit", "library_ids", "media_scope"} {
		if strings.Contains(got, banned) {
			t.Fatalf("canonical fragment must not contain %q, got %q", banned, got)
		}
	}

	var def QueryDefinition
	if err := json.Unmarshal([]byte(got), &def); err != nil {
		t.Fatalf("canonical fragment does not round-trip: %v", err)
	}
	if def.Match != "all" || len(def.Groups) != 1 || len(def.Groups[0].Rules) != 2 {
		t.Fatalf("unexpected canonical shape: %+v", def)
	}
}

func TestNormalizeDisplayQueryFragment_RejectsExecutionControllingFields(t *testing.T) {
	cases := map[string]string{
		"limit":       `{"match":"all","limit":5,"groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
		"sort":        `{"match":"all","sort":{"field":"title","order":"asc"},"groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
		"library_ids": `{"match":"all","library_ids":[3],"groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
		"media_scope": `{"match":"all","media_scope":"movie","groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`,
	}
	for name, raw := range cases {
		if _, err := NormalizeDisplayQueryFragment([]byte(raw)); err == nil {
			t.Fatalf("%s: expected rejection of execution-controlling field, got nil error", name)
		}
	}
}

func TestNormalizeDisplayQueryFragment_RejectsUnsupportedFieldsAndValues(t *testing.T) {
	cases := map[string]string{
		"unsupported field": `{"match":"all","groups":[{"match":"all","rules":[{"field":"genre","op":"is","value":"Drama"}]}]}`,
		"watched non-bool":  `{"match":"all","groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":"yes"}]}]}`,
		"type non-string":   `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":3}]}]}`,
		"type empty string": `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"  "}]}]}`,
		"malformed json":    `{"match":"all",`,
	}
	for name, raw := range cases {
		if _, err := NormalizeDisplayQueryFragment([]byte(raw)); err == nil {
			t.Fatalf("%s: expected error, got nil", name)
		}
	}
}

package handlers

import (
	"testing"
)

func TestNormalizeQueryDefinitionJSON_RejectsPersonalizedSortWithoutProfileScope(t *testing.T) {
	raw := []byte(`{"match":"all","groups":[],"sort":{"field":"progress","order":"desc"}}`)

	if _, err := normalizeQueryDefinitionJSON(raw, false, true, false); err == nil {
		t.Fatal("expected personalized sort to be rejected for admin/global validation")
	}
}

func TestNormalizeQueryDefinitionJSON_RejectsPersonalizedFieldWithoutProfileScope(t *testing.T) {
	raw := []byte(`{"match":"all","groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`)

	if _, err := normalizeQueryDefinitionJSON(raw, true, false, false); err == nil {
		t.Fatal("expected personalized field to be rejected for non-profile validation")
	}
}

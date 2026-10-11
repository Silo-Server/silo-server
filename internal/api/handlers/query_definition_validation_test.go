package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/sections"
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

func TestV1SavesKeepTheFrozenRuleVocabulary(t *testing.T) {
	// /api/v1 collection and section saves refuse the rules /api/v2 added,
	// with the messages those routes gave before; /api/v2 saves accept them.
	for _, tc := range []struct {
		name, raw, sectionError string
	}{
		{"title", `{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"contains","value":"x"}]}]}`, `groups[0].rules[0].field "title" is not supported`},
		{"not_in_last", `{"match":"all","groups":[{"match":"all","rules":[{"field":"release_date","op":"not_in_last","value":"1y"}]}]}`, `groups[0].rules[0].op "not_in_last" is not supported for field "release_date"`},
	} {
		raw := []byte(tc.raw)
		if _, err := normalizeQueryDefinitionJSON(raw, true, true, true); err == nil {
			t.Errorf("%s: a v1 collection save accepted the rule", tc.name)
		}
		if _, err := normalizeSmartCollectionQueryDefinitionJSON(raw, true, true, true); err == nil {
			t.Errorf("%s: a v1 Smart collection save accepted the rule", tc.name)
		}
		if _, err := normalizeQueryDefinitionJSON(raw, true, true, false); err != nil {
			t.Errorf("%s: a v2 collection save refused the rule: %v", tc.name, err)
		}
		if msg, ok := validateSectionConfig(sections.SectionCustomFilter, raw, true); ok || msg != tc.sectionError {
			t.Errorf("%s: v1 section save = %q, %v; want %q", tc.name, msg, ok, tc.sectionError)
		}
		if msg, ok := validateSectionConfig(sections.SectionCustomFilter, raw, false); !ok {
			t.Errorf("%s: v2 section save refused the rule: %s", tc.name, msg)
		}
	}
}

package auth

import (
	"strings"
	"testing"
)

func TestScanACLRuleFields(t *testing.T) {
	row := aclRuleRow{
		ID:           42,
		SubjectType:  "group",
		SubjectID:    "viewer",
		Action:       "playback.play",
		ResourceType: "library",
		ResourceID:   "10",
		Effect:       "allow",
		Priority:     5,
		Name:         "viewer playback",
		Description:  "allows library playback",
	}

	rule, err := row.toRule()
	if err != nil {
		t.Fatalf("toRule() error = %v", err)
	}
	if rule.ID != 42 {
		t.Fatalf("id = %d, want 42", rule.ID)
	}
	if rule.SubjectType != SubjectGroup {
		t.Fatalf("subject type = %q, want %q", rule.SubjectType, SubjectGroup)
	}
	if rule.Action != ActionPlaybackPlay {
		t.Fatalf("action = %q, want %q", rule.Action, ActionPlaybackPlay)
	}
	if rule.ResourceType != ResourceLibrary {
		t.Fatalf("resource type = %q, want %q", rule.ResourceType, ResourceLibrary)
	}
	if rule.Effect != EffectAllow {
		t.Fatalf("effect = %q, want %q", rule.Effect, EffectAllow)
	}
}

func TestACLRuleConditionsDecodeObject(t *testing.T) {
	row := aclRuleRow{
		Conditions: []byte(`{"LibraryIDs":[10],"MediaTypes":["movie"]}`),
	}

	rule, err := row.toRule()
	if err != nil {
		t.Fatalf("toRule() error = %v", err)
	}
	if len(rule.Conditions.LibraryIDs) != 1 || rule.Conditions.LibraryIDs[0] != 10 {
		t.Fatalf("library ids = %#v, want [10]", rule.Conditions.LibraryIDs)
	}
	if len(rule.Conditions.MediaTypes) != 1 || rule.Conditions.MediaTypes[0] != "movie" {
		t.Fatalf("media types = %#v, want [movie]", rule.Conditions.MediaTypes)
	}
}

func TestACLRuleConditionsRejectNonObject(t *testing.T) {
	row := aclRuleRow{Conditions: []byte(`[]`)}

	if _, err := row.toRule(); err == nil {
		t.Fatalf("toRule() error = nil, want non-object conditions to fail")
	}
}

func TestACLRuleBuiltInRoleMapping(t *testing.T) {
	row := aclRuleRow{
		SubjectType: "builtin_role",
		SubjectID:   string(GroupAdmin),
	}

	rule, err := row.toRule()
	if err != nil {
		t.Fatalf("toRule() error = %v", err)
	}
	if rule.SubjectType != SubjectBuiltInRole {
		t.Fatalf("subject type = %q, want %q", rule.SubjectType, SubjectBuiltInRole)
	}
	if rule.SubjectID != string(GroupAdmin) {
		t.Fatalf("subject id = %q, want %q", rule.SubjectID, GroupAdmin)
	}
}

func TestACLRepositoryListRulesForUserQueryUsesBuiltInMemberships(t *testing.T) {
	if !strings.Contains(aclRulesForUserQuery, "subject_type = 'builtin_role'") {
		t.Fatalf("query missing builtin role filter: %s", aclRulesForUserQuery)
	}
	if !strings.Contains(aclRulesForUserQuery, "g.built_in = true") {
		t.Fatalf("query missing built-in group guard: %s", aclRulesForUserQuery)
	}
	if strings.Contains(aclRulesForUserQuery, "users.role") {
		t.Fatalf("query still depends on users.role: %s", aclRulesForUserQuery)
	}
}

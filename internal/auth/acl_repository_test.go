package auth

import "testing"

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

	rule := row.toRule()
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

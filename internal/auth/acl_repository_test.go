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
		Conditions: []byte(`{
			"library_ids":[10],
			"media_types":["movie"],
			"primary_profile_required":true,
			"max_playback_quality":"1080p",
			"max_streams":2,
			"max_transcodes":1,
			"direct_downloads_allowed":true,
			"transcoded_downloads_allowed":false,
			"max_content_rating":"PG-13"
		}`),
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
	if rule.Conditions.PrimaryProfileRequired == nil || !*rule.Conditions.PrimaryProfileRequired {
		t.Fatalf("primary profile required = %#v, want true", rule.Conditions.PrimaryProfileRequired)
	}
	if rule.Conditions.MaxPlaybackQuality != "1080p" {
		t.Fatalf("max playback quality = %q, want 1080p", rule.Conditions.MaxPlaybackQuality)
	}
	if rule.Conditions.MaxStreams == nil || *rule.Conditions.MaxStreams != 2 {
		t.Fatalf("max streams = %#v, want 2", rule.Conditions.MaxStreams)
	}
	if rule.Conditions.MaxTranscodes == nil || *rule.Conditions.MaxTranscodes != 1 {
		t.Fatalf("max transcodes = %#v, want 1", rule.Conditions.MaxTranscodes)
	}
	if rule.Conditions.DirectDownloadsAllowed == nil || !*rule.Conditions.DirectDownloadsAllowed {
		t.Fatalf("direct downloads allowed = %#v, want true", rule.Conditions.DirectDownloadsAllowed)
	}
	if rule.Conditions.TranscodedDownloadsAllowed == nil || *rule.Conditions.TranscodedDownloadsAllowed {
		t.Fatalf("transcoded downloads allowed = %#v, want false", rule.Conditions.TranscodedDownloadsAllowed)
	}
	if rule.Conditions.MaxContentRating != "PG-13" {
		t.Fatalf("max content rating = %q, want PG-13", rule.Conditions.MaxContentRating)
	}
}

func TestACLRuleConditionsRejectNonObject(t *testing.T) {
	row := aclRuleRow{Conditions: []byte(`[]`)}

	if _, err := row.toRule(); err == nil {
		t.Fatalf("toRule() error = nil, want non-object conditions to fail")
	}
}

func TestACLRuleConditionsRejectUnknownKeys(t *testing.T) {
	row := aclRuleRow{
		Conditions: []byte(`{"library_ids":[10],"library_ids_typo":[20]}`),
	}

	if _, err := row.toRule(); err == nil {
		t.Fatalf("toRule() error = nil, want unknown condition keys to fail")
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

package auth

import "testing"

func TestACLEvaluatorDisabledUserDenied(t *testing.T) {
	evaluator := NewACLEvaluator()
	decision := evaluator.Authorize(AccessRequest{Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "1"}, nil, EffectivePolicy{}, false)
	if decision.Allowed {
		t.Fatalf("disabled user should be denied")
	}
	if decision.ReasonCode != "user_disabled" {
		t.Fatalf("reason = %q, want user_disabled", decision.ReasonCode)
	}
}

func TestACLEvaluatorUserDenyBeatsUserAllow(t *testing.T) {
	evaluator := NewACLEvaluator()
	request := AccessRequest{UserID: 7, Action: ActionDownloadsDirect, ResourceType: ResourceLibrary, ResourceID: "10"}
	rules := []ACLRule{
		{ID: 1, SubjectType: SubjectUser, SubjectID: "7", Action: ActionDownloadsDirect, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 10, Name: "allow direct downloads"},
		{ID: 2, SubjectType: SubjectUser, SubjectID: "7", Action: ActionDownloadsDirect, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectDeny, Priority: 10, Name: "deny direct downloads"},
	}

	decision := evaluator.Authorize(request, rules, EffectivePolicy{}, true)
	if decision.Allowed {
		t.Fatalf("user deny should win over user allow")
	}
	if decision.WinningRule == nil || decision.WinningRule.ID != 2 {
		t.Fatalf("winning rule = %#v, want rule 2", decision.WinningRule)
	}
}

func TestACLEvaluatorUserAllowBeatsGroupDeny(t *testing.T) {
	evaluator := NewACLEvaluator()
	request := AccessRequest{UserID: 7, Action: ActionMetadataCurate, ResourceType: ResourceLibrary, ResourceID: "10"}
	rules := []ACLRule{
		{ID: 1, SubjectType: SubjectGroup, SubjectID: "curators", Action: ActionMetadataCurate, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectDeny, Priority: 10, Name: "group deny"},
		{ID: 2, SubjectType: SubjectUser, SubjectID: "7", Action: ActionMetadataCurate, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 10, Name: "user override"},
	}

	decision := evaluator.Authorize(request, rules, EffectivePolicy{}, true)
	if !decision.Allowed {
		t.Fatalf("user allow should beat group deny: %#v", decision)
	}
	if decision.WinningRule == nil || decision.WinningRule.ID != 2 {
		t.Fatalf("winning rule = %#v, want rule 2", decision.WinningRule)
	}
}

func TestACLEvaluatorExplainIncludesMatchedRules(t *testing.T) {
	evaluator := NewACLEvaluator()
	request := AccessRequest{UserID: 7, Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10"}
	rules := []ACLRule{
		{ID: 1, SubjectType: SubjectGroup, SubjectID: "viewer", Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 10, Name: "viewer playback"},
	}

	explanation := evaluator.Explain(request, rules, EffectivePolicy{MaxStreams: 2}, true)
	if !explanation.Decision.Allowed {
		t.Fatalf("expected allowed decision: %#v", explanation.Decision)
	}
	if len(explanation.Decision.MatchedRules) != 1 {
		t.Fatalf("matched rules = %#v, want one rule", explanation.Decision.MatchedRules)
	}
	if explanation.Decision.EffectivePolicy.MaxStreams != 2 {
		t.Fatalf("effective max streams = %d, want 2", explanation.Decision.EffectivePolicy.MaxStreams)
	}
}

func TestACLEvaluatorConditionFactsBlockUnmetConstraints(t *testing.T) {
	evaluator := NewACLEvaluator()

	maxStreams := 2
	maxTranscodes := 1
	allowDirectDownloads := false
	allowTranscodedDownloads := false

	tests := []struct {
		name    string
		request AccessRequest
		rule    ACLRule
	}{
		{
			name: "playback quality ceiling",
			request: AccessRequest{
				Action:          ActionPlaybackPlay,
				ResourceType:    ResourceLibrary,
				ResourceID:      "10",
				PlaybackQuality: "2160p",
			},
			rule: ACLRule{
				ID:           10,
				SubjectType:  SubjectGroup,
				SubjectID:    "viewer",
				Action:       ActionPlaybackPlay,
				ResourceType: ResourceLibrary,
				ResourceID:   "10",
				Effect:       EffectAllow,
				Priority:     10,
				Conditions:   ACLCondition{MaxPlaybackQuality: "1080p"},
			},
		},
		{
			name: "stream ceiling",
			request: AccessRequest{
				Action:         ActionPlaybackPlay,
				ResourceType:   ResourceLibrary,
				ResourceID:     "10",
				CurrentStreams: 3,
			},
			rule: ACLRule{
				ID:           11,
				SubjectType:  SubjectGroup,
				SubjectID:    "viewer",
				Action:       ActionPlaybackPlay,
				ResourceType: ResourceLibrary,
				ResourceID:   "10",
				Effect:       EffectAllow,
				Priority:     10,
				Conditions:   ACLCondition{MaxStreams: &maxStreams},
			},
		},
		{
			name: "transcode ceiling",
			request: AccessRequest{
				Action:            ActionPlaybackTranscode,
				ResourceType:      ResourceLibrary,
				ResourceID:        "10",
				CurrentTranscodes: 2,
			},
			rule: ACLRule{
				ID:           12,
				SubjectType:  SubjectGroup,
				SubjectID:    "viewer",
				Action:       ActionPlaybackTranscode,
				ResourceType: ResourceLibrary,
				ResourceID:   "10",
				Effect:       EffectAllow,
				Priority:     10,
				Conditions:   ACLCondition{MaxTranscodes: &maxTranscodes},
			},
		},
		{
			name: "direct download flag",
			request: AccessRequest{
				Action:                  ActionDownloadsDirect,
				ResourceType:            ResourceLibrary,
				ResourceID:              "10",
				DirectDownloadRequested: true,
			},
			rule: ACLRule{
				ID:           13,
				SubjectType:  SubjectGroup,
				SubjectID:    "viewer",
				Action:       ActionDownloadsDirect,
				ResourceType: ResourceLibrary,
				ResourceID:   "10",
				Effect:       EffectAllow,
				Priority:     10,
				Conditions:   ACLCondition{DirectDownloadsAllowed: &allowDirectDownloads},
			},
		},
		{
			name: "transcoded download flag",
			request: AccessRequest{
				Action:                      ActionDownloadsTranscode,
				ResourceType:                ResourceLibrary,
				ResourceID:                  "10",
				TranscodedDownloadRequested: true,
			},
			rule: ACLRule{
				ID:           14,
				SubjectType:  SubjectGroup,
				SubjectID:    "viewer",
				Action:       ActionDownloadsTranscode,
				ResourceType: ResourceLibrary,
				ResourceID:   "10",
				Effect:       EffectAllow,
				Priority:     10,
				Conditions:   ACLCondition{TranscodedDownloadsAllowed: &allowTranscodedDownloads},
			},
		},
		{
			name: "content rating ceiling",
			request: AccessRequest{
				Action:        ActionPlaybackPlay,
				ResourceType:  ResourceLibrary,
				ResourceID:    "10",
				ContentRating: "TV-MA",
			},
			rule: ACLRule{
				ID:           15,
				SubjectType:  SubjectGroup,
				SubjectID:    "viewer",
				Action:       ActionPlaybackPlay,
				ResourceType: ResourceLibrary,
				ResourceID:   "10",
				Effect:       EffectAllow,
				Priority:     10,
				Conditions:   ACLCondition{MaxContentRating: "PG-13"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision := evaluator.Authorize(tc.request, []ACLRule{tc.rule}, EffectivePolicy{}, true)
			if decision.Allowed {
				t.Fatalf("expected rule to be blocked: %#v", decision)
			}
			if decision.ReasonCode != "default_deny" {
				t.Fatalf("reason code = %q, want default_deny", decision.ReasonCode)
			}
			if len(decision.MatchedRules) != 0 {
				t.Fatalf("matched rules = %#v, want none", decision.MatchedRules)
			}
		})
	}
}

func TestACLEvaluatorConditionFactsIgnoreMissingRequestValues(t *testing.T) {
	evaluator := NewACLEvaluator()
	maxStreams := 2
	maxTranscodes := 1
	allowDirectDownloads := true
	allowTranscodedDownloads := true

	decision := evaluator.Authorize(
		AccessRequest{Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10"},
		[]ACLRule{
			{
				ID:           20,
				SubjectType:  SubjectGroup,
				SubjectID:    "viewer",
				Action:       ActionPlaybackPlay,
				ResourceType: ResourceLibrary,
				ResourceID:   "10",
				Effect:       EffectAllow,
				Priority:     10,
				Conditions: ACLCondition{
					MaxPlaybackQuality:         "1080p",
					MaxStreams:                 &maxStreams,
					MaxTranscodes:              &maxTranscodes,
					DirectDownloadsAllowed:     &allowDirectDownloads,
					TranscodedDownloadsAllowed: &allowTranscodedDownloads,
					MaxContentRating:           "PG-13",
				},
			},
		},
		EffectivePolicy{},
		true,
	)

	if !decision.Allowed {
		t.Fatalf("missing request facts should not block matching rule: %#v", decision)
	}
	if decision.ReasonCode != "rule_allow" {
		t.Fatalf("reason code = %q, want rule_allow", decision.ReasonCode)
	}
}

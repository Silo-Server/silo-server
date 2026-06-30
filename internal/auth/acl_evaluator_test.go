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
		{ID: 1, SubjectType: SubjectGroup, SubjectID: string(GroupStandardUser), Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 10, Name: "user playback"},
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
				SubjectID:    string(GroupStandardUser),
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
				SubjectID:    string(GroupStandardUser),
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
				SubjectID:    string(GroupStandardUser),
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
				SubjectID:    string(GroupStandardUser),
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
				SubjectID:    string(GroupStandardUser),
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
				SubjectID:    string(GroupStandardUser),
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
				SubjectID:    string(GroupStandardUser),
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

func TestACLEvaluatorExplainSnapshotsRequestAndPolicySlices(t *testing.T) {
	evaluator := NewACLEvaluator()

	request := AccessRequest{
		Action:       ActionPlaybackPlay,
		ResourceType: ResourceLibrary,
		ResourceID:   "10",
		LibraryIDs:   []int{10},
	}
	basePolicy := EffectivePolicy{
		LibraryIDs: []int{10},
		MediaTypes: []string{"movie"},
	}

	explanation := evaluator.Explain(
		request,
		[]ACLRule{
			{
				ID:           30,
				SubjectType:  SubjectGroup,
				SubjectID:    string(GroupStandardUser),
				Action:       ActionPlaybackPlay,
				ResourceType: ResourceLibrary,
				ResourceID:   "10",
				Effect:       EffectAllow,
				Priority:     10,
			},
		},
		basePolicy,
		true,
	)

	request.LibraryIDs[0] = 99
	basePolicy.LibraryIDs[0] = 99
	basePolicy.MediaTypes[0] = "series"

	if len(explanation.Request.LibraryIDs) != 1 || explanation.Request.LibraryIDs[0] != 10 {
		t.Fatalf("request library ids = %#v, want [10]", explanation.Request.LibraryIDs)
	}
	if len(explanation.Decision.EffectivePolicy.LibraryIDs) != 1 || explanation.Decision.EffectivePolicy.LibraryIDs[0] != 10 {
		t.Fatalf("effective policy library ids = %#v, want [10]", explanation.Decision.EffectivePolicy.LibraryIDs)
	}
	if len(explanation.Decision.EffectivePolicy.MediaTypes) != 1 || explanation.Decision.EffectivePolicy.MediaTypes[0] != "movie" {
		t.Fatalf("effective policy media types = %#v, want [movie]", explanation.Decision.EffectivePolicy.MediaTypes)
	}
}

func TestACLEvaluatorExplainDeepCopiesPointerConditions(t *testing.T) {
	evaluator := NewACLEvaluator()

	primaryProfileRequired := true
	maxStreams := 2
	maxTranscodes := 1
	directDownloadsAllowed := true
	transcodedDownloadsAllowed := false

	rules := []ACLRule{
		{
			ID:           31,
			SubjectType:  SubjectUser,
			SubjectID:    "7",
			Action:       ActionPlaybackPlay,
			ResourceType: ResourceLibrary,
			ResourceID:   "10",
			Effect:       EffectAllow,
			Priority:     10,
			Conditions: ACLCondition{
				PrimaryProfileRequired:     &primaryProfileRequired,
				MaxStreams:                 &maxStreams,
				MaxTranscodes:              &maxTranscodes,
				DirectDownloadsAllowed:     &directDownloadsAllowed,
				TranscodedDownloadsAllowed: &transcodedDownloadsAllowed,
			},
		},
	}

	explanation := evaluator.Explain(
		AccessRequest{
			UserID:         7,
			Action:         ActionPlaybackPlay,
			ResourceType:   ResourceLibrary,
			ResourceID:     "10",
			LibraryIDs:     []int{10},
			PrimaryProfile: true,
		},
		rules,
		EffectivePolicy{},
		true,
	)

	primaryProfileRequired = false
	maxStreams = 9
	maxTranscodes = 8
	directDownloadsAllowed = false
	transcodedDownloadsAllowed = true

	evaluated := explanation.EvaluatedRules[0].Conditions
	if evaluated.PrimaryProfileRequired == nil || !*evaluated.PrimaryProfileRequired {
		t.Fatalf("evaluated primary profile required = %#v, want true", evaluated.PrimaryProfileRequired)
	}
	if evaluated.MaxStreams == nil || *evaluated.MaxStreams != 2 {
		t.Fatalf("evaluated max streams = %#v, want 2", evaluated.MaxStreams)
	}
	if evaluated.MaxTranscodes == nil || *evaluated.MaxTranscodes != 1 {
		t.Fatalf("evaluated max transcodes = %#v, want 1", evaluated.MaxTranscodes)
	}
	if evaluated.DirectDownloadsAllowed == nil || !*evaluated.DirectDownloadsAllowed {
		t.Fatalf("evaluated direct downloads allowed = %#v, want true", evaluated.DirectDownloadsAllowed)
	}
	if evaluated.TranscodedDownloadsAllowed == nil || *evaluated.TranscodedDownloadsAllowed {
		t.Fatalf("evaluated transcoded downloads allowed = %#v, want false", evaluated.TranscodedDownloadsAllowed)
	}

	matched := explanation.Decision.MatchedRules[0].Conditions
	if matched.PrimaryProfileRequired == nil || !*matched.PrimaryProfileRequired {
		t.Fatalf("matched primary profile required = %#v, want true", matched.PrimaryProfileRequired)
	}
	if matched.MaxStreams == nil || *matched.MaxStreams != 2 {
		t.Fatalf("matched max streams = %#v, want 2", matched.MaxStreams)
	}
	if explanation.Decision.WinningRule == nil {
		t.Fatalf("winning rule = nil, want rule snapshot")
	}
	if explanation.Decision.WinningRule.Conditions.MaxTranscodes == nil || *explanation.Decision.WinningRule.Conditions.MaxTranscodes != 1 {
		t.Fatalf("winning rule max transcodes = %#v, want 1", explanation.Decision.WinningRule.Conditions.MaxTranscodes)
	}
	if explanation.Decision.WinningRule.Conditions.DirectDownloadsAllowed == nil || !*explanation.Decision.WinningRule.Conditions.DirectDownloadsAllowed {
		t.Fatalf("winning rule direct downloads allowed = %#v, want true", explanation.Decision.WinningRule.Conditions.DirectDownloadsAllowed)
	}
	if explanation.Decision.WinningRule.Conditions.TranscodedDownloadsAllowed == nil || *explanation.Decision.WinningRule.Conditions.TranscodedDownloadsAllowed {
		t.Fatalf("winning rule transcoded downloads allowed = %#v, want false", explanation.Decision.WinningRule.Conditions.TranscodedDownloadsAllowed)
	}
}

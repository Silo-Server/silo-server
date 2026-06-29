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

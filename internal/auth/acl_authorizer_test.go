package auth

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeACLUserLoader struct {
	user *models.User
}

func (f fakeACLUserLoader) GetByID(ctx context.Context, id int) (*models.User, error) {
	return f.user, nil
}

type fakeACLRuleLoader struct {
	rules []ACLRule
}

func (f fakeACLRuleLoader) ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error) {
	return f.rules, nil
}

type fakeACLPolicyRuleLoader struct {
	rules    []ACLRule
	policies []ACLPolicy
}

func (f fakeACLPolicyRuleLoader) ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error) {
	return f.rules, nil
}

func (f fakeACLPolicyRuleLoader) ListPoliciesForUser(ctx context.Context, userID int) ([]ACLPolicy, error) {
	return f.policies, nil
}

type trackingACLRuleLoader struct {
	calls int
	err   error
	rules []ACLRule
}

func (f *trackingACLRuleLoader) ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.rules, nil
}

func TestACLAuthorizerCombinesRepositoryAndCompatibilityRules(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{"marker_edit"}, MaxStreams: 2}
	ruleLoader := fakeACLRuleLoader{
		rules: []ACLRule{
			{ID: 99, SubjectType: SubjectGroup, SubjectID: string(GroupStandardUser), Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 1, Name: "user playback"},
			{ID: 100, SubjectType: SubjectUser, SubjectID: "7", Action: ActionMarkersEdit, ResourceType: ResourceServer, ResourceID: "*", Effect: EffectAllow, Priority: 2, Name: "repository marker edit"},
		},
	}

	authorizer := NewACLAuthorizer(ruleLoader, fakeACLUserLoader{user: user})
	decision, err := authorizer.Authorize(context.Background(), AccessRequest{UserID: 7, Action: ActionMarkersEdit, ResourceType: ResourceServer, ResourceID: "*"})
	if err != nil {
		t.Fatalf("authorize error: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("legacy marker_edit should allow markers.edit: %#v", decision)
	}
	if decision.EffectivePolicy.MaxStreams != 2 {
		t.Fatalf("effective max streams = %d, want 2", decision.EffectivePolicy.MaxStreams)
	}
	if len(decision.MatchedRules) != 2 {
		t.Fatalf("matched rules = %#v, want repository and compatibility rules", decision.MatchedRules)
	}
	foundRepositoryRule := false
	for _, rule := range decision.MatchedRules {
		if rule.ID == 100 {
			foundRepositoryRule = true
		}
	}
	if !foundRepositoryRule {
		t.Fatalf("matched rules = %#v, want repository rule 100 to be included", decision.MatchedRules)
	}
}

func TestACLAuthorizerCascadesGroupPolicyByMostPermissiveValue(t *testing.T) {
	maxProfiles := 4
	fourStreams := 4
	fiveStreams := 5
	oneTranscode := 1
	user := &models.User{
		ID:                       7,
		Role:                     "user",
		Enabled:                  true,
		MaxPlaybackQuality:       "1080p",
		MaxStreams:               6,
		MaxTranscodes:            2,
		MaxProfiles:              5,
		DownloadAllowed:          false,
		DownloadTranscodeAllowed: false,
	}
	ruleLoader := fakeACLPolicyRuleLoader{
		rules: []ACLRule{
			{ID: 1, SubjectType: SubjectGroup, SubjectID: string(GroupStandardUser), Action: ActionServerView, ResourceType: ResourceServer, ResourceID: "*", Effect: EffectAllow},
		},
		policies: []ACLPolicy{
			{MaxProfiles: &maxProfiles},
			{MaxStreams: &fourStreams},
			{MaxStreams: &fiveStreams, MaxTranscodes: &oneTranscode},
		},
	}

	authorizer := NewACLAuthorizer(ruleLoader, fakeACLUserLoader{user: user})
	decision, err := authorizer.Authorize(context.Background(), AccessRequest{UserID: 7, Action: ActionServerView, ResourceType: ResourceServer, ResourceID: "*"})
	if err != nil {
		t.Fatalf("authorize error: %v", err)
	}

	if !decision.Allowed {
		t.Fatalf("server.view should be allowed: %#v", decision)
	}
	if decision.EffectivePolicy.MaxProfiles != 4 {
		t.Fatalf("max profiles = %d, want 4", decision.EffectivePolicy.MaxProfiles)
	}
	if decision.EffectivePolicy.MaxStreams != 5 {
		t.Fatalf("max streams = %d, want 5", decision.EffectivePolicy.MaxStreams)
	}
	if decision.EffectivePolicy.MaxTranscodes != 1 {
		t.Fatalf("max transcodes = %d, want 1", decision.EffectivePolicy.MaxTranscodes)
	}
	if decision.EffectivePolicy.MaxPlaybackQuality != "1080p" {
		t.Fatalf("max quality = %q, want legacy fallback 1080p", decision.EffectivePolicy.MaxPlaybackQuality)
	}
}

func TestACLAuthorizerDisabledUserShortCircuitsBeforeRuleLoad(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: false}
	ruleLoader := &trackingACLRuleLoader{err: context.Canceled}

	authorizer := NewACLAuthorizer(ruleLoader, fakeACLUserLoader{user: user})
	decision, err := authorizer.Authorize(context.Background(), AccessRequest{UserID: 7, Action: ActionMarkersEdit, ResourceType: ResourceServer, ResourceID: "*"})
	if err != nil {
		t.Fatalf("authorize error: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("disabled user should be denied: %#v", decision)
	}
	if decision.ReasonCode != "user_disabled" {
		t.Fatalf("reason = %q, want user_disabled", decision.ReasonCode)
	}
	if ruleLoader.calls != 0 {
		t.Fatalf("rule loader calls = %d, want 0", ruleLoader.calls)
	}
}

func TestACLAuthorizerExplainIncludesEvaluatedRules(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true}
	ruleLoader := fakeACLRuleLoader{
		rules: []ACLRule{
			{ID: 77, SubjectType: SubjectUser, SubjectID: "7", Action: ActionPlaybackPlay, ResourceType: ResourceServer, ResourceID: "*", Effect: EffectAllow, Priority: 5, Name: "repository playback"},
		},
	}

	authorizer := NewACLAuthorizer(ruleLoader, fakeACLUserLoader{user: user})
	explanation, err := authorizer.Explain(context.Background(), AccessRequest{UserID: 7, Action: ActionPlaybackPlay, ResourceType: ResourceServer, ResourceID: "*"})
	if err != nil {
		t.Fatalf("explain error: %v", err)
	}
	if !explanation.Decision.Allowed {
		t.Fatalf("expected allowed explanation: %#v", explanation.Decision)
	}
	if len(explanation.EvaluatedRules) != 1 {
		t.Fatalf("evaluated rules = %#v, want one repository rule", explanation.EvaluatedRules)
	}
	if explanation.EvaluatedRules[0].ID != 77 {
		t.Fatalf("evaluated rule id = %d, want 77", explanation.EvaluatedRules[0].ID)
	}
}

func TestACLAuthorizerLegacyMetadataCurationRespectsLibraryScope(t *testing.T) {
	user := &models.User{
		ID:          7,
		Role:        "user",
		Enabled:     true,
		Permissions: []string{"metadata_curation"},
		LibraryIDs:  []int{10},
	}

	authorizer := NewACLAuthorizer(fakeACLRuleLoader{}, fakeACLUserLoader{user: user})

	allowedDecision, err := authorizer.Authorize(context.Background(), AccessRequest{
		UserID:       7,
		Action:       ActionMetadataCurate,
		ResourceType: ResourceLibrary,
		ResourceID:   "10",
		LibraryIDs:   []int{10},
	})
	if err != nil {
		t.Fatalf("authorize allowed request error: %v", err)
	}
	if !allowedDecision.Allowed {
		t.Fatalf("expected in-scope library request to be allowed: %#v", allowedDecision)
	}

	deniedDecision, err := authorizer.Authorize(context.Background(), AccessRequest{
		UserID:       7,
		Action:       ActionMetadataCurate,
		ResourceType: ResourceLibrary,
		ResourceID:   "20",
		LibraryIDs:   []int{20},
	})
	if err != nil {
		t.Fatalf("authorize denied request error: %v", err)
	}
	if deniedDecision.Allowed {
		t.Fatalf("expected out-of-scope library request to be denied: %#v", deniedDecision)
	}
	if deniedDecision.ReasonCode != "default_deny" {
		t.Fatalf("reason code = %q, want default_deny", deniedDecision.ReasonCode)
	}

	partialOverlapDecision, err := authorizer.Authorize(context.Background(), AccessRequest{
		UserID:       7,
		Action:       ActionMetadataCurate,
		ResourceType: ResourceMediaItem,
		ResourceID:   "item-1",
		LibraryIDs:   []int{10, 20},
	})
	if err != nil {
		t.Fatalf("authorize partial-overlap request error: %v", err)
	}
	if partialOverlapDecision.Allowed {
		t.Fatalf("expected partially out-of-scope multi-library request to be denied: %#v", partialOverlapDecision)
	}
	if partialOverlapDecision.ReasonCode != "default_deny" {
		t.Fatalf("partial-overlap reason code = %q, want default_deny", partialOverlapDecision.ReasonCode)
	}
}

func TestACLAuthorizerLegacyMetadataCurationDeniedWhenLibraryScopeEmpty(t *testing.T) {
	user := &models.User{
		ID:          7,
		Role:        "user",
		Enabled:     true,
		Permissions: []string{"metadata_curation"},
		LibraryIDs:  []int{},
	}

	authorizer := NewACLAuthorizer(fakeACLRuleLoader{}, fakeACLUserLoader{user: user})
	decision, err := authorizer.Authorize(context.Background(), AccessRequest{
		UserID:       7,
		Action:       ActionMetadataCurate,
		ResourceType: ResourceLibrary,
		ResourceID:   "10",
		LibraryIDs:   []int{10},
	})
	if err != nil {
		t.Fatalf("authorize error: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("expected empty library scope to deny library request: %#v", decision)
	}
	if decision.ReasonCode != "default_deny" {
		t.Fatalf("reason code = %q, want default_deny", decision.ReasonCode)
	}
}

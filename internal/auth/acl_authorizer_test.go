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
			{ID: 99, SubjectType: SubjectGroup, SubjectID: "viewer", Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 1, Name: "viewer playback"},
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

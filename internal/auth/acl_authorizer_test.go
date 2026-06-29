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

func TestACLAuthorizerCombinesRepositoryAndCompatibilityRules(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{"marker_edit"}, MaxStreams: 2}
	ruleLoader := fakeACLRuleLoader{
		rules: []ACLRule{
			{ID: 99, SubjectType: SubjectGroup, SubjectID: "viewer", Action: ActionPlaybackPlay, ResourceType: ResourceLibrary, ResourceID: "10", Effect: EffectAllow, Priority: 1, Name: "viewer playback"},
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
}

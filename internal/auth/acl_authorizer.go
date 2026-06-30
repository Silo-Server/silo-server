package auth

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

type Authorizer interface {
	Authorize(ctx context.Context, request AccessRequest) (AccessDecision, error)
	Explain(ctx context.Context, request AccessRequest) (AccessExplanation, error)
}

type UserLoaderForACL interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type ACLRuleLoader interface {
	ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error)
}

type ACLPolicyLoader interface {
	ListPoliciesForUser(ctx context.Context, userID int) ([]ACLPolicy, error)
}

type ACLAuthorizer struct {
	rules     ACLRuleLoader
	users     UserLoaderForACL
	evaluator *ACLEvaluator
}

func NewACLAuthorizer(rules ACLRuleLoader, users UserLoaderForACL) *ACLAuthorizer {
	return &ACLAuthorizer{
		rules:     rules,
		users:     users,
		evaluator: NewACLEvaluator(),
	}
}

func (a *ACLAuthorizer) Authorize(ctx context.Context, request AccessRequest) (AccessDecision, error) {
	user, rules, policy, err := a.loadInputs(ctx, request.UserID)
	if err != nil {
		return AccessDecision{}, err
	}
	return a.evaluator.Authorize(request, rules, policy, user != nil && user.Enabled), nil
}

func (a *ACLAuthorizer) Explain(ctx context.Context, request AccessRequest) (AccessExplanation, error) {
	user, rules, policy, err := a.loadInputs(ctx, request.UserID)
	if err != nil {
		return AccessExplanation{}, err
	}
	return a.evaluator.Explain(request, rules, policy, user != nil && user.Enabled), nil
}

func (a *ACLAuthorizer) loadInputs(ctx context.Context, userID int) (*models.User, []ACLRule, EffectivePolicy, error) {
	user, err := a.users.GetByID(ctx, userID)
	if err != nil {
		return nil, nil, EffectivePolicy{}, err
	}
	if user == nil || !user.Enabled {
		return user, nil, EffectivePolicy{}, nil
	}

	rules := []ACLRule{}
	if a.rules != nil {
		repositoryRules, err := a.rules.ListRulesForUser(ctx, userID)
		if err != nil {
			return nil, nil, EffectivePolicy{}, err
		}
		rules = append(rules, repositoryRules...)
	}
	rules = append(rules, CompatibilityRulesForUser(user)...)
	policy := CompatibilityEffectivePolicyForUser(user)
	if loader, ok := a.rules.(ACLPolicyLoader); ok {
		policies, err := loader.ListPoliciesForUser(ctx, userID)
		if err != nil {
			return nil, nil, EffectivePolicy{}, err
		}
		policy = MergeACLPolicies(policy, policies)
	}

	return user, rules, policy, nil
}

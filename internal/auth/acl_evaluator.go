package auth

import (
	"fmt"
	"sort"
)

type ACLEvaluator struct{}

func NewACLEvaluator() *ACLEvaluator {
	return &ACLEvaluator{}
}

func (e *ACLEvaluator) Authorize(request AccessRequest, rules []ACLRule, basePolicy EffectivePolicy, userEnabled bool) AccessDecision {
	if !userEnabled {
		return AccessDecision{Allowed: false, ReasonCode: "user_disabled", EffectivePolicy: basePolicy}
	}

	matched := matchingRules(request, rules)
	sortMatchedRules(matched)
	if len(matched) == 0 {
		return AccessDecision{Allowed: false, ReasonCode: "default_deny", MatchedRules: matched, EffectivePolicy: basePolicy}
	}

	winning := matched[0]
	allowed := winning.Effect == EffectAllow
	reason := "rule_allow"
	if !allowed {
		reason = "rule_deny"
	}

	return AccessDecision{
		Allowed:         allowed,
		ReasonCode:      reason,
		WinningRule:     &winning,
		MatchedRules:    matched,
		EffectivePolicy: basePolicy,
	}
}

func (e *ACLEvaluator) Explain(request AccessRequest, rules []ACLRule, basePolicy EffectivePolicy, userEnabled bool) AccessExplanation {
	decision := e.Authorize(request, rules, basePolicy, userEnabled)
	return AccessExplanation{
		Request:        request,
		Decision:       decision,
		EvaluatedRules: append([]ACLRule(nil), rules...),
	}
}

func matchingRules(request AccessRequest, rules []ACLRule) []ACLRule {
	out := make([]ACLRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Action != request.Action {
			continue
		}
		if !resourceMatches(request, rule) {
			continue
		}
		if !conditionsMatch(request, rule.Conditions) {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func resourceMatches(request AccessRequest, rule ACLRule) bool {
	if rule.ResourceType != request.ResourceType && rule.ResourceType != ResourceServer {
		return false
	}
	if rule.ResourceID == "" || rule.ResourceID == "*" {
		return true
	}
	if request.ResourceID == "" {
		return false
	}
	return rule.ResourceID == request.ResourceID
}

func conditionsMatch(request AccessRequest, conditions ACLCondition) bool {
	if len(conditions.LibraryIDs) > 0 {
		ok := false
		for _, allowed := range conditions.LibraryIDs {
			for _, requested := range request.LibraryIDs {
				if allowed == requested {
					ok = true
					break
				}
			}
			if ok {
				break
			}
		}
		if !ok {
			return false
		}
	}

	if len(conditions.MediaTypes) > 0 {
		ok := false
		for _, mediaType := range conditions.MediaTypes {
			if mediaType == request.MediaType {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}

	if conditions.PrimaryProfileRequired != nil && *conditions.PrimaryProfileRequired != request.PrimaryProfile {
		return false
	}

	return true
}

func sortMatchedRules(rules []ACLRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		left := ruleRank(rules[i])
		right := ruleRank(rules[j])
		if left != right {
			return left < right
		}
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority > rules[j].Priority
		}
		return rules[i].ID < rules[j].ID
	})
}

func ruleRank(rule ACLRule) int {
	switch {
	case rule.SubjectType == SubjectBuiltInRole && rule.SubjectID == string(GroupOwner) && rule.Effect == EffectAllow:
		return 0
	case rule.SubjectType == SubjectUser && rule.Effect == EffectDeny:
		return 1
	case rule.SubjectType == SubjectUser && rule.Effect == EffectAllow:
		return 2
	case rule.SubjectType == SubjectGroup && rule.Effect == EffectDeny:
		return 3
	case rule.SubjectType == SubjectGroup && rule.Effect == EffectAllow:
		return 4
	case rule.Effect == EffectDeny:
		return 5
	case rule.Effect == EffectAllow:
		return 6
	default:
		panic(fmt.Sprintf("unknown ACL effect %q", rule.Effect))
	}
}

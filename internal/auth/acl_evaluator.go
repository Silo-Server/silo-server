package auth

import (
	"fmt"
	"sort"
	"strings"
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
		EvaluatedRules: cloneACLRules(rules),
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
		out = append(out, cloneACLRule(rule))
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

	if conditions.MaxPlaybackQuality != "" {
		if request.PlaybackQuality == "" {
			// Caller has not supplied this fact yet.
		} else if !playbackQualityAllowed(request.PlaybackQuality, conditions.MaxPlaybackQuality) {
			return false
		}
	}

	if conditions.MaxStreams != nil {
		if request.CurrentStreams != 0 && request.CurrentStreams > *conditions.MaxStreams {
			return false
		}
	}

	if conditions.MaxTranscodes != nil {
		if request.CurrentTranscodes != 0 && request.CurrentTranscodes > *conditions.MaxTranscodes {
			return false
		}
	}

	if conditions.DirectDownloadsAllowed != nil && !*conditions.DirectDownloadsAllowed && request.DirectDownloadRequested {
		return false
	}

	if conditions.TranscodedDownloadsAllowed != nil && !*conditions.TranscodedDownloadsAllowed && request.TranscodedDownloadRequested {
		return false
	}

	if conditions.MaxContentRating != "" {
		if request.ContentRating == "" {
			// Caller has not supplied this fact yet.
		} else if !contentRatingAllowed(request.ContentRating, conditions.MaxContentRating) {
			return false
		}
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

var playbackQualityRanks = map[string]int{
	"480P":  1,
	"720P":  2,
	"1080P": 3,
	"2160P": 4,
	"4320P": 5,
}

var contentRatingRanks = map[string]int{
	"G":     1,
	"TV-G":  1,
	"PG":    2,
	"TV-PG": 2,
	"PG-13": 3,
	"TV-14": 3,
	"R":     4,
	"TV-MA": 4,
	"NC-17": 5,
}

func playbackQualityAllowed(requested, max string) bool {
	requestRank, ok := playbackQualityRank(requested)
	if !ok {
		return false
	}
	maxRank, ok := playbackQualityRank(max)
	if !ok {
		return false
	}
	return requestRank <= maxRank
}

func contentRatingAllowed(requested, max string) bool {
	requestRank, ok := contentRatingRank(requested)
	if !ok {
		return false
	}
	maxRank, ok := contentRatingRank(max)
	if !ok {
		return false
	}
	return requestRank <= maxRank
}

func playbackQualityRank(value string) (int, bool) {
	rank, ok := playbackQualityRanks[strings.ToUpper(strings.TrimSpace(value))]
	return rank, ok
}

func contentRatingRank(value string) (int, bool) {
	rank, ok := contentRatingRanks[strings.ToUpper(strings.TrimSpace(value))]
	return rank, ok
}

func cloneACLRules(rules []ACLRule) []ACLRule {
	if rules == nil {
		return nil
	}
	out := make([]ACLRule, len(rules))
	for i, rule := range rules {
		out[i] = cloneACLRule(rule)
	}
	return out
}

func cloneACLRule(rule ACLRule) ACLRule {
	cloned := rule
	cloned.Conditions = cloneACLCondition(rule.Conditions)
	return cloned
}

func cloneACLCondition(condition ACLCondition) ACLCondition {
	cloned := condition
	if condition.LibraryIDs != nil {
		cloned.LibraryIDs = append([]int(nil), condition.LibraryIDs...)
	}
	if condition.MediaTypes != nil {
		cloned.MediaTypes = append([]string(nil), condition.MediaTypes...)
	}
	return cloned
}

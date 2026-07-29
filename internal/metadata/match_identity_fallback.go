package metadata

import (
	"strconv"
	"strings"
)

const maxProviderIdentityFallbacks = 3

func compactAlternateMatchIdentities(hints *MatchHints) []MatchIdentityHint {
	if hints == nil || len(hints.AlternateIdentities) == 0 {
		return nil
	}

	seen := map[string]struct{}{
		matchIdentityComparisonKey(hints.Title, hints.Year): {},
	}
	out := make([]MatchIdentityHint, 0, min(len(hints.AlternateIdentities), maxProviderIdentityFallbacks))
	for _, alternate := range hints.AlternateIdentities {
		alternate.Title = strings.Join(strings.Fields(strings.TrimSpace(alternate.Title)), " ")
		key := matchIdentityComparisonKey(alternate.Title, alternate.Year)
		if alternate.Title == "" || key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, alternate)
		if len(out) == maxProviderIdentityFallbacks {
			break
		}
	}
	return out
}

func matchIdentityComparisonKey(title string, year int) string {
	normalized := strings.Join(strings.Fields(normalizeTitleForScoring(title)), " ")
	if normalized == "" {
		return ""
	}
	return normalized + "\x00" + strconv.Itoa(year)
}

func bestCandidateScore(hints *MatchHints, candidates []MatchCandidate) float64 {
	best := 0.0
	for _, candidate := range candidates {
		score, _, _ := scoreMatchCandidateDetailed(hints, candidate)
		if score > best {
			best = score
		}
	}
	return best
}

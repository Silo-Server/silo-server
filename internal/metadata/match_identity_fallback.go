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
	// An explicit movie year is a hard constraint unless the alternate is the
	// numeric-title hypothesis that absorbs hints.Year back into the title.
	preserveMovieYear := hints.Year != 0 && isMovieTypeAlias(strings.ToLower(strings.TrimSpace(hints.Type)))
	hintYearSuffix := ""
	if preserveMovieYear {
		hintYearSuffix = " " + strconv.Itoa(hints.Year)
	}
	out := make([]MatchIdentityHint, 0, min(len(hints.AlternateIdentities), maxProviderIdentityFallbacks))
	for _, alternate := range hints.AlternateIdentities {
		alternate.Title = strings.Join(strings.Fields(strings.TrimSpace(alternate.Title)), " ")
		isNumericTitleForHintYear := alternate.Source == numericTitleHintSource && strings.HasSuffix(alternate.Title, hintYearSuffix)
		if preserveMovieYear && alternate.Year == 0 && !isNumericTitleForHintYear {
			alternate.Year = hints.Year
		}
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

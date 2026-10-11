package metadata

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// Only a handful of same-year results ever reach this path: a title the
	// search scored as unrelated is rarely shared by many films from one year.
	// More than this is too ambiguous to settle with a detail fetch each.
	maxTitleAliasValidationCandidates = 3
	titleAliasValidationTimeout       = 10 * time.Second
)

// validateMatchByProviderTitleAliases accepts a search result whose search
// titles don't resemble the local title but whose full provider title list
// does. Search results carry only the localized and original titles, so a
// folder named with a regional release title ("首尔之春 (2023)" for TMDB's
// "12.12: The Day", original "서울의 봄") scores as unrelated even when the
// provider returned the right film. The provider's detail response lists the
// alternative titles; an exact match there, with the same year, is the
// provider confirming the folder's title.
//
// It is positive-only and conservative: every same-year contender is checked,
// exactly one must list the title, and a failed detail fetch other than a 404
// leaves the item unmatched, since an unreachable rival could have listed it
// too. A search that already returned an exact-title result was rejected as
// ambiguous, not as unrelated, and is left alone.
func validateMatchByProviderTitleAliases(
	ctx context.Context,
	hints *MatchHints,
	candidates []MatchCandidate,
	providers []Provider,
	language string,
) (*MatchCandidate, []error) {
	if hints == nil || hints.Year == 0 || normalizeTitleForScoring(hints.Title) == "" || trustedHintIDsPresent(hints) {
		return nil, nil
	}

	contenders := make([]MatchCandidate, 0, maxTitleAliasValidationCandidates)
	for _, candidate := range candidates {
		// Only a remote result can be an exact-title rival. A title-only NFO
		// echoes the folder title by design, and selection already set aside
		// ID-less candidates once remote results exist.
		if providerIDRichness(candidate.ProviderIDs) == 0 || candidateCorroboratingSourceCount(candidate) == 0 {
			continue
		}
		if similarity, _ := bestCandidateTitleSimilarity(hints.Title, candidate, hints.Year); similarity == 1 {
			return nil, nil
		}
		if candidate.Year != hints.Year ||
			len(candidate.ConflictingProviderIDKeys) > 0 ||
			!candidateTypeMatchesHint(hints.Type, candidate.ContentType) {
			continue
		}
		if len(contenders) == maxTitleAliasValidationCandidates {
			return nil, nil
		}
		contenders = append(contenders, candidate)
	}
	if len(contenders) == 0 {
		return nil, nil
	}

	validationCtx, cancel := context.WithTimeout(ctx, titleAliasValidationTimeout)
	defer cancel()

	var winner *MatchCandidate
	for _, candidate := range contenders {
		matchedTitle, err := providerTitleAliasMatch(validationCtx, hints, candidate, providers, language)
		if err != nil {
			return nil, []error{err}
		}
		if matchedTitle == "" {
			continue
		}
		if winner != nil {
			return nil, nil
		}
		confirmed := candidate
		confirmed.MatchedTitle = matchedTitle
		confirmed.MatchReasons = append(append([]string(nil), candidate.MatchReasons...), "provider_title_alias")
		winner = &confirmed
	}
	return winner, nil
}

// providerTitleAliasMatch fetches the candidate's details from the providers
// that returned it and reports which of their titles matches the local title
// exactly, or "" when none does. A provider that no longer knows the ID (404)
// simply doesn't confirm it. A local sidecar provider never confirms a remote
// result, so only corroborating sources are asked.
func providerTitleAliasMatch(
	ctx context.Context,
	hints *MatchHints,
	candidate MatchCandidate,
	providers []Provider,
	language string,
) (string, error) {
	for _, p := range providers {
		mp, ok := p.(MetadataProvider)
		if !ok || nonCorroboratingSources[p.Slug()] || !candidateHasSource(candidate, p.Slug()) {
			continue
		}
		result, err := mp.GetMetadata(ctx, MetadataRequest{
			ProviderIDs: candidate.ProviderIDs,
			ContentType: candidate.ContentType,
			Language:    language,
		})
		if isProvider404(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("title alias validation via %s: %w", p.Slug(), err)
		}
		if result == nil {
			continue
		}
		titles := []string{result.Title, result.OriginalTitle}
		for _, alias := range result.TitleAliases {
			titles = append(titles, alias.Title)
		}
		for _, title := range titles {
			if strings.TrimSpace(title) != "" && inferTitleSimilarity(hints.Title, title, hints.Year) == 1 {
				return title, nil
			}
		}
	}
	return "", nil
}

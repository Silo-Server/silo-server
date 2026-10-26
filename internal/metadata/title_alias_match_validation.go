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
	// More than this is too ambiguous to settle with a detail fetch each. The
	// cap and the timeout cover one item across all of its identity attempts.
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
// budget holds how many candidates the item may still check; each call spends
// what it checks, and a call needing more than is left checks nothing. A nil
// budget allows one full cap.
//
// It is positive-only and conservative: every same-year contender is checked
// and exactly one must list the title. A rival counts as ruled out only when a
// provider no longer knows it (404) or returns its complete title list without
// the title. A rival with conflicting provider IDs, a failed fetch, or a partial
// title list leaves the item unmatched, since it could have listed the title
// too. A search that already returned an exact-title result near the folder's
// year was rejected as ambiguous, not as unrelated, and is left alone.
func validateMatchByProviderTitleAliases(
	ctx context.Context,
	hints *MatchHints,
	candidates []MatchCandidate,
	providers []Provider,
	language string,
	budget *int,
) (*MatchCandidate, []error) {
	if hints == nil || hints.Year == 0 || normalizeTitleForScoring(hints.Title) == "" || trustedHintIDsPresent(hints) {
		return nil, nil
	}

	limit := maxTitleAliasValidationCandidates
	if budget != nil {
		limit = *budget
	}
	contenders := make([]MatchCandidate, 0, maxTitleAliasValidationCandidates)
	for _, candidate := range candidates {
		// Only a remote result can be an exact-title rival. A title-only NFO
		// echoes the folder title by design, and selection already set aside
		// ID-less candidates once remote results exist.
		if providerIDRichness(candidate.ProviderIDs) == 0 || candidateCorroboratingSourceCount(candidate) == 0 {
			continue
		}
		// An exact title within the release-date window selection tolerates
		// (or with no year) is a real rival; one from years away is another work.
		if similarity, _ := bestCandidateTitleSimilarity(hints.Title, candidate, hints.Year); similarity == 1 &&
			(candidate.Year == 0 || absYearDelta(candidate.Year, hints.Year) <= 2) {
			return nil, nil
		}
		if candidate.Year != hints.Year || !candidateTypeMatchesHint(hints.Type, candidate.ContentType) {
			continue
		}
		// Its details would come from IDs that point at different works.
		if len(candidate.ConflictingProviderIDKeys) > 0 {
			return nil, nil
		}
		if len(contenders) == limit {
			return nil, nil
		}
		contenders = append(contenders, candidate)
	}
	if len(contenders) == 0 {
		return nil, nil
	}
	if budget != nil {
		*budget -= len(contenders)
	}

	validationCtx, cancel := context.WithTimeout(ctx, titleAliasValidationTimeout)
	defer cancel()

	var winner *MatchCandidate
	for _, candidate := range contenders {
		matchedTitle, ruledOut, err := providerTitleAliasMatch(validationCtx, hints, candidate, providers, language)
		if err != nil {
			return nil, []error{err}
		}
		if matchedTitle == "" {
			if !ruledOut {
				return nil, nil
			}
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
// exactly. Any one provider can confirm it. When none does, ruledOut says
// whether that is established: every provider asked returned a complete title
// list without it or no longer knows the ID (404). A local sidecar provider
// never confirms a remote result, so only corroborating sources are asked.
func providerTitleAliasMatch(
	ctx context.Context,
	hints *MatchHints,
	candidate MatchCandidate,
	providers []Provider,
	language string,
) (matchedTitle string, ruledOut bool, err error) {
	asked, unresolved := 0, false
	for _, p := range providers {
		mp, ok := p.(MetadataProvider)
		if !ok || nonCorroboratingSources[p.Slug()] || !candidateHasSource(candidate, p.Slug()) {
			continue
		}
		asked++
		result, fetchErr := mp.GetMetadata(ctx, MetadataRequest{
			ProviderIDs: candidate.ProviderIDs,
			ContentType: candidate.ContentType,
			Language:    language,
		})
		if isProvider404(fetchErr) {
			continue
		}
		if fetchErr != nil {
			return "", false, fmt.Errorf("title alias validation via %s: %w", p.Slug(), fetchErr)
		}
		// A response without metadata is no answer, whatever fields it carries,
		// and one that dates the work to another year contradicts the search.
		if result == nil || !result.HasMetadata || (result.Year != 0 && result.Year != hints.Year) {
			unresolved = true
			continue
		}
		titles := []string{result.Title, result.OriginalTitle}
		for _, alias := range result.TitleAliases {
			titles = append(titles, alias.Title)
		}
		for _, title := range titles {
			if strings.TrimSpace(title) != "" && inferTitleSimilarity(hints.Title, title, hints.Year) == 1 {
				return title, true, nil
			}
		}
		if !result.TitleAliasesComplete {
			unresolved = true
		}
	}
	return "", asked > 0 && !unresolved, nil
}

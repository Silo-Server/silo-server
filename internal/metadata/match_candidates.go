package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/providerid"
)

// MatchCandidate represents a deduplicated search result grouped by normalized
// provider IDs. Multiple raw SearchResult rows from different providers that
// share the same TMDB/TVDB/IMDB IDs are collapsed into a single candidate.
type MatchCandidate struct {
	Title           string            `json:"title"`
	OriginalTitle   string            `json:"original_title,omitempty"`
	TitleAliases    []TitleAlias      `json:"aliases,omitempty"`
	TitleLanguage   string            `json:"title_language,omitempty"`
	TitleIsFallback bool              `json:"title_is_fallback,omitempty"`
	MatchedTitle    string            `json:"matched_title,omitempty"`
	MatchScore      float64           `json:"match_score,omitempty"`
	MatchReasons    []string          `json:"match_reasons,omitempty"`
	Year            int               `json:"year"`
	ContentType     string            `json:"content_type"`
	ProviderIDs     map[string]string `json:"provider_ids"`
	ImageURL        string            `json:"image_url,omitempty"`
	Overview        string            `json:"overview,omitempty"`
	Sources         []string          `json:"sources"`
	AgreementHints  []string          `json:"agreement_hints"`
	DetailScore     int               `json:"-"`
	// ConfirmedProviderIDs contains provider IDs returned by their owning
	// provider (for example, tmdb from a TMDB result). It arbitrates conflicting
	// cross-references without rejecting compatible IDs from aggregators or
	// third-party providers.
	ConfirmedProviderIDs map[string]string `json:"-"`
	// ConflictingProviderIDKeys contains canonical provider IDs excluded during
	// candidate selection after two other canonical IDs proved that provider
	// results refer to the same work. A value in ConfirmedProviderIDs resolves
	// the conflict; otherwise the key remains quarantined through persistence.
	ConflictingProviderIDKeys []string `json:"-"`
	titleRank                 int
}

var canonicalCandidateIDKeys = []string{"tmdb", "tvdb", "imdb"}

func providerIDMergeEvidence(left, right map[string]string) (matches int, conflicts []string) {
	for _, key := range canonicalCandidateIDKeys {
		lv := strings.TrimSpace(left[key])
		rv := strings.TrimSpace(right[key])
		if lv == "" || rv == "" {
			continue
		}
		if lv != rv {
			conflicts = append(conflicts, key)
			continue
		}
		matches++
	}
	return matches, conflicts
}

func compatibleProviderIDs(left, right map[string]string) bool {
	matches, conflicts := providerIDMergeEvidence(left, right)
	if matches == 0 {
		return false
	}
	// One agreeing ID plus one conflict is not consensus. Two independently
	// agreeing canonical IDs are enough to prove identity while quarantining a
	// stale third-party cross-reference.
	return len(conflicts) == 0 || matches >= 2
}

func providerIDRichness(ids map[string]string) int {
	score := 0
	for _, key := range canonicalCandidateIDKeys {
		if strings.TrimSpace(ids[key]) != "" {
			score++
		}
	}
	return score
}

func sanitizeCandidateProviderIDs(ids map[string]string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	sanitized := make(map[string]string, len(ids))
	for key, value := range ids {
		key = strings.ToLower(strings.TrimSpace(key))
		value, valid := sanitizeProviderIDValue(key, value)
		if !valid {
			continue
		}
		sanitized[key] = value
	}
	return sanitized
}

func sanitizeCanonicalProviderIDsInPlace(ids map[string]string) {
	for _, key := range canonicalCandidateIDKeys {
		value := strings.TrimSpace(ids[key])
		if value == "" {
			delete(ids, key)
			continue
		}
		sanitized, valid := sanitizeProviderIDValue(key, value)
		if !valid {
			delete(ids, key)
			continue
		}
		ids[key] = sanitized
	}
}

func sanitizeProviderIDValue(key, value string) (string, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return "", false
	}
	switch key {
	case "tmdb", "tvdb":
		if !providerid.IsPositiveDecimal(value) {
			return "", false
		}
	case "imdb":
		value = strings.ToLower(value)
		if !isValidIMDbProviderID(value) {
			return "", false
		}
	}
	return value, true
}

func sanitizedMatchHintProviderIDs(hints *MatchHints) *MatchHints {
	if hints == nil {
		return nil
	}
	sanitized := *hints
	sanitized.TmdbID, _ = sanitizeProviderIDValue("tmdb", hints.TmdbID)
	sanitized.TvdbID, _ = sanitizeProviderIDValue("tvdb", hints.TvdbID)
	sanitized.ImdbID, _ = sanitizeProviderIDValue("imdb", hints.ImdbID)
	return &sanitized
}

func isValidIMDbProviderID(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, "tt") {
		return false
	}
	digits := strings.TrimPrefix(value, "tt")
	return len(digits) >= 7 && len(digits) <= 10 && providerid.IsPositiveDecimal(digits)
}

const (
	automaticMatchAcceptanceFloor = 55
	minimumDetailTieBreakScore    = 20
	minimumDetailTieBreakGap      = 12
)

func duplicateTieBreakWinner(hints *MatchHints, scoredCandidates []scoredMatchCandidate) (*MatchCandidate, bool) {
	if hints == nil || len(scoredCandidates) < 2 {
		return nil, false
	}

	best := scoredCandidates[0]
	contenders := []scoredMatchCandidate{best}
	for i := 1; i < len(scoredCandidates); i++ {
		next := scoredCandidates[i]
		if best.score-next.score >= 15 {
			break
		}
		if duplicateTieBreakComparable(hints, best.candidate, next.candidate) {
			contenders = append(contenders, next)
		}
	}
	if len(contenders) < 2 {
		return nil, false
	}

	sort.SliceStable(contenders, func(i, j int) bool {
		return contenders[i].candidate.DetailScore > contenders[j].candidate.DetailScore
	})
	if contenders[0].candidate.DetailScore < minimumDetailTieBreakScore {
		return nil, false
	}
	if contenders[0].candidate.DetailScore-contenders[1].candidate.DetailScore < minimumDetailTieBreakGap {
		return nil, false
	}
	return &contenders[0].candidate, true
}

func duplicateTieBreakComparable(hints *MatchHints, left, right MatchCandidate) bool {
	if hints == nil {
		return false
	}
	if hints.Year == 0 || left.Year == 0 || right.Year == 0 {
		return false
	}
	if left.Year != hints.Year || right.Year != hints.Year || left.Year != right.Year {
		return false
	}
	if !candidateTypeMatchesHint(hints.Type, left.ContentType) ||
		!candidateTypeMatchesHint(hints.Type, right.ContentType) {
		return false
	}
	if strings.TrimSpace(left.ContentType) != "" &&
		strings.TrimSpace(right.ContentType) != "" &&
		!strings.EqualFold(left.ContentType, right.ContentType) {
		return false
	}
	if candidateToCandidateTitleSimilarity(left, right, hints.Year) != 1 {
		return false
	}
	if similarity, _ := bestCandidateTitleSimilarity(hints.Title, left, hints.Year); similarity != 1 {
		return false
	}
	if similarity, _ := bestCandidateTitleSimilarity(hints.Title, right, hints.Year); similarity != 1 {
		return false
	}
	return samePrimaryProvider(left.ProviderIDs, right.ProviderIDs)
}

func candidateTypeMatchesHint(hintType, candidateType string) bool {
	hintType = strings.ToLower(strings.TrimSpace(hintType))
	candidateType = strings.ToLower(strings.TrimSpace(candidateType))
	if hintType == "" || candidateType == "" {
		return true
	}
	if hintType == candidateType {
		return true
	}
	return isMovieTypeAlias(hintType) && isMovieTypeAlias(candidateType)
}

func isMovieTypeAlias(value string) bool {
	switch value {
	case "movie", "movies":
		return true
	default:
		return false
	}
}

func samePrimaryProvider(left, right map[string]string) bool {
	for _, key := range canonicalCandidateIDKeys {
		leftValue := strings.TrimSpace(left[key])
		rightValue := strings.TrimSpace(right[key])
		if leftValue != "" && rightValue != "" {
			return true
		}
	}
	return false
}

// normalizedKey returns a stable grouping key from provider IDs.
// Results with identical provider ID fingerprints (the exact set of
// tmdb/tvdb/imdb key=value pairs) are considered the same candidate.
func normalizedKey(ids map[string]string) string {
	var parts []string
	for _, k := range canonicalCandidateIDKeys {
		if v, ok := ids[k]; ok && v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	if len(parts) == 0 {
		// Fall back to metadb if present.
		if v, ok := ids["metadb"]; ok && v != "" {
			return "metadb=" + v
		}
		return ""
	}
	return strings.Join(parts, ",")
}

// NormalizeCandidates deduplicates raw search results into MatchCandidate
// entries. Results with identical provider ID fingerprints are merged:
// provider IDs are unioned, sources list every provider slug that returned
// the result, and agreement_hints notes when multiple providers agree.
func NormalizeCandidates(results []SearchResult, contentType string) []MatchCandidate {
	return NormalizeCandidatesForLanguage(results, contentType, "")
}

// NormalizeCandidatesForLanguage deduplicates results while preferring an
// explicitly localized provider title over a provider-marked fallback.
func NormalizeCandidatesForLanguage(results []SearchResult, contentType, language string) []MatchCandidate {
	type bucket struct {
		candidate                  MatchCandidate
		sources                    map[string]bool
		conflictingIDKeys          map[string]bool
		confirmedIDValues          map[string]string
		conflictingConfirmedIDKeys map[string]bool
	}

	ordered := make([]string, 0)
	buckets := make(map[string]*bucket)

	for _, sr := range results {
		sr.ProviderIDs = sanitizeCandidateProviderIDs(sr.ProviderIDs)
		key := ""
		for _, existingKey := range ordered {
			if compatibleProviderIDs(buckets[existingKey].candidate.ProviderIDs, sr.ProviderIDs) {
				key = existingKey
				break
			}
		}
		if key == "" {
			key = normalizedKey(sr.ProviderIDs)
		}
		if key == "" {
			// Cannot group by provider IDs; create a synthetic unique key.
			key = sr.Provider + ":" + sr.Name + ":" + strings.Repeat("?", len(ordered))
		}

		b, exists := buckets[key]
		if !exists {
			title, titleLanguage, fallback, titleRank := preferredSearchResultTitle(sr, language)
			b = &bucket{
				candidate: MatchCandidate{
					Title:                title,
					OriginalTitle:        sr.OriginalTitle,
					TitleAliases:         copyTitleAliases(sr.TitleAliases, sr.Provider),
					TitleLanguage:        titleLanguage,
					TitleIsFallback:      fallback,
					titleRank:            titleRank,
					Year:                 sr.Year,
					ContentType:          contentType,
					ProviderIDs:          make(map[string]string),
					ConfirmedProviderIDs: make(map[string]string),
					ImageURL:             sr.ImageURL,
					Overview:             sr.Overview,
				},
				sources:                    make(map[string]bool),
				conflictingIDKeys:          make(map[string]bool),
				confirmedIDValues:          make(map[string]string),
				conflictingConfirmedIDKeys: make(map[string]bool),
			}
			buckets[key] = b
			ordered = append(ordered, key)
		}
		mergeCandidateTitles(&b.candidate, sr, language)

		// Merge provider IDs. When two canonical IDs agree but a third
		// conflicts, the candidates are the same work and the disputed key is
		// quarantined instead of allowing provider iteration order to choose it.
		for k, v := range sr.ProviderIDs {
			v = strings.TrimSpace(v)
			if v == "" || b.conflictingIDKeys[k] {
				continue
			}
			if existing := strings.TrimSpace(b.candidate.ProviderIDs[k]); existing != "" && existing != v && slices.Contains(canonicalCandidateIDKeys, k) {
				delete(b.candidate.ProviderIDs, k)
				b.conflictingIDKeys[k] = true
				continue
			}
			b.candidate.ProviderIDs[k] = v
		}

		// Track source providers and the ID returned by the provider that owns
		// that namespace. Conflicting native results are not authoritative.
		if sr.Provider != "" {
			b.sources[sr.Provider] = true
			provider := strings.ToLower(strings.TrimSpace(sr.Provider))
			if slices.Contains(canonicalCandidateIDKeys, provider) && strings.TrimSpace(sr.ProviderIDs[provider]) != "" {
				value := strings.TrimSpace(sr.ProviderIDs[provider])
				if existing := b.confirmedIDValues[provider]; existing != "" && existing != value {
					delete(b.confirmedIDValues, provider)
					b.conflictingConfirmedIDKeys[provider] = true
				} else if !b.conflictingConfirmedIDKeys[provider] {
					b.confirmedIDValues[provider] = value
				}
			}
		}

		// Prefer non-empty overview and image.
		if b.candidate.Overview == "" && sr.Overview != "" {
			b.candidate.Overview = sr.Overview
		}
		if b.candidate.ImageURL == "" && sr.ImageURL != "" {
			b.candidate.ImageURL = sr.ImageURL
		}
		b.candidate.DetailScore = candidateDetailScore(b.candidate)
	}

	// Build final list preserving insertion order.
	candidates := make([]MatchCandidate, 0, len(ordered))
	for _, key := range ordered {
		b := buckets[key]
		// Flatten sources.
		sources := make([]string, 0, len(b.sources))
		for s := range b.sources {
			sources = append(sources, s)
		}
		sort.Strings(sources)
		b.candidate.Sources = sources
		for _, key := range canonicalCandidateIDKeys {
			if value := b.confirmedIDValues[key]; value != "" {
				b.candidate.ConfirmedProviderIDs[key] = value
			}
			if b.conflictingIDKeys[key] {
				b.candidate.ConflictingProviderIDKeys = append(b.candidate.ConflictingProviderIDKeys, key)
				if b.candidate.ConfirmedProviderIDs[key] != "" {
					b.candidate.AgreementHints = append(b.candidate.AgreementHints, "resolved_"+key+"_id")
				} else {
					b.candidate.AgreementHints = append(b.candidate.AgreementHints, "quarantined_"+key+"_id")
				}
			}
		}

		// Compute agreement hints from corroborating sources only: a local
		// sidecar (nfo) echoing a remote result is not provider agreement.
		corroborating := make([]string, 0, len(sources))
		for _, s := range sources {
			if !nonCorroboratingSources[strings.ToLower(s)] {
				corroborating = append(corroborating, s)
			}
		}
		if len(corroborating) > 1 {
			b.candidate.AgreementHints = append(b.candidate.AgreementHints,
				"agreed_by_"+strings.Join(corroborating, "_and_"))
		}

		candidates = append(candidates, b.candidate)
	}

	return candidates
}

func preferredSearchResultTitle(result SearchResult, language string) (string, string, bool, int) {
	requested := baseMetadataLanguage(language)
	resultLanguage := baseMetadataLanguage(result.TitleLanguage)
	if strings.TrimSpace(result.Name) != "" && requested != "" && resultLanguage == requested && !result.TitleIsFallback {
		return result.Name, resultLanguage, false, 3
	}
	for _, alias := range result.TitleAliases {
		if strings.TrimSpace(alias.Title) != "" && requested != "" && baseMetadataLanguage(alias.Language) == requested {
			return alias.Title, requested, false, 2
		}
	}
	// Older plugins predate the language contract. Their primary title keeps
	// normal first-provider priority; its original_title must not silently
	// replace a title that may already be localized.
	if strings.TrimSpace(result.Name) != "" && resultLanguage == "" && !result.TitleIsFallback {
		return result.Name, "", false, 3
	}
	if requested != "" && strings.TrimSpace(result.OriginalTitle) != "" {
		return result.OriginalTitle, baseMetadataLanguage(result.OriginalLanguage), true, 1
	}
	if strings.TrimSpace(result.Name) != "" {
		return result.Name, resultLanguage, result.TitleIsFallback, 1
	}
	return result.OriginalTitle, baseMetadataLanguage(result.OriginalLanguage), true, 1
}

func baseMetadataLanguage(language string) string {
	return lang.Canonical(strings.ReplaceAll(language, "_", "-"))
}

func copyTitleAliases(aliases []TitleAlias, provider string) []TitleAlias {
	out := make([]TitleAlias, 0, len(aliases))
	for _, alias := range aliases {
		if alias.Provider == "" {
			alias.Provider = provider
		}
		out = appendUniqueTitleAlias(out, alias)
	}
	return out
}

func mergeCandidateTitles(candidate *MatchCandidate, result SearchResult, language string) {
	if candidate == nil {
		return
	}
	if candidate.OriginalTitle == "" && strings.TrimSpace(result.OriginalTitle) != "" {
		candidate.OriginalTitle = result.OriginalTitle
	}
	for _, alias := range copyTitleAliases(result.TitleAliases, result.Provider) {
		candidate.TitleAliases = appendUniqueTitleAlias(candidate.TitleAliases, alias)
	}
	if strings.TrimSpace(result.OriginalTitle) != "" && !strings.EqualFold(result.OriginalTitle, candidate.Title) {
		candidate.TitleAliases = appendUniqueTitleAlias(candidate.TitleAliases, TitleAlias{
			Title: result.OriginalTitle, Language: baseMetadataLanguage(result.OriginalLanguage), Kind: titleAliasKindOriginal, Provider: result.Provider,
		})
	}
	if strings.TrimSpace(result.Name) != "" && !strings.EqualFold(result.Name, candidate.Title) {
		candidate.TitleAliases = appendUniqueTitleAlias(candidate.TitleAliases, TitleAlias{
			Title: result.Name, Language: baseMetadataLanguage(result.TitleLanguage), Kind: titleAliasKindLocalized, Provider: result.Provider,
		})
	}

	title, titleLanguage, fallback, titleRank := preferredSearchResultTitle(result, language)
	if strings.TrimSpace(title) != "" && titleRank > candidate.titleRank {
		candidate.Title = title
		candidate.TitleLanguage = titleLanguage
		candidate.TitleIsFallback = fallback
		candidate.titleRank = titleRank
	}
}

func appendUniqueTitleAlias(aliases []TitleAlias, alias TitleAlias) []TitleAlias {
	alias.Title = strings.TrimSpace(alias.Title)
	if alias.Title == "" {
		return aliases
	}
	for _, existing := range aliases {
		if strings.EqualFold(existing.Title, alias.Title) &&
			baseMetadataLanguage(existing.Language) == baseMetadataLanguage(alias.Language) &&
			strings.EqualFold(existing.Kind, alias.Kind) &&
			strings.EqualFold(existing.Provider, alias.Provider) {
			return aliases
		}
	}
	return append(aliases, alias)
}

func candidateDetailScore(candidate MatchCandidate) int {
	score := providerIDRichness(candidate.ProviderIDs) * 10
	if candidate.Year != 0 {
		score += 15
	}
	if strings.TrimSpace(candidate.Overview) != "" {
		score += 20
	}
	if strings.TrimSpace(candidate.ImageURL) != "" {
		score += 15
	}
	return score
}

// SearchAndNormalize is a convenience method that calls SearchProviders and
// normalizes the results into MatchCandidates. Plugin-prefixed image URLs
// (e.g. "metadb://...") are resolved to presigned HTTP URLs before returning.
func (s *MetadataService) SearchAndNormalize(ctx context.Context, query SearchQuery, folderID int) ([]MatchCandidate, error) {
	if strings.TrimSpace(query.Language) == "" {
		query.Language = s.resolveFolderLanguage(ctx, folderID)
	}
	results, err := s.SearchProviders(ctx, query, folderID)
	if err != nil {
		return nil, err
	}
	candidates := NormalizeCandidatesForLanguage(results, query.ContentType, query.Language)
	for i := range candidates {
		annotateCandidateMatch(&candidates[i], &MatchHints{Title: query.Title, Year: query.Year, Type: query.ContentType})
	}

	if s.imageResolver != nil {
		for i, c := range candidates {
			if c.ImageURL != "" && strings.Contains(c.ImageURL, "://") {
				resolved := s.imageResolver.ResolveImageURL(ctx, c.ImageURL, "card")
				if resolved != "" {
					candidates[i].ImageURL = resolved
				}
			}
		}
	}

	return candidates, nil
}

func scoreMatchCandidate(hints *MatchHints, candidate MatchCandidate) float64 {
	score, _, _ := scoreMatchCandidateDetailed(hints, candidate)
	return score
}

func scoreMatchCandidateDetailed(hints *MatchHints, candidate MatchCandidate) (float64, string, []string) {
	if hints == nil {
		return 0, "", nil
	}

	score := 0.0
	reasons := make([]string, 0, 5)
	trustedIDMatches := 0
	for _, key := range trustedSearchIDKeys {
		hintValue := trustedIDValue(hints, key)
		if hintValue == "" {
			continue
		}
		if candidate.ProviderIDs[key] == hintValue {
			score += 100
			trustedIDMatches++
			reasons = append(reasons, "trusted_"+key+"_id")
		}
	}
	if trustedIDMatches > 0 {
		score += float64(trustedIDMatches * 10)
	}

	if sourceCount := candidateCorroboratingSourceCount(candidate); sourceCount > 0 {
		score += float64(sourceCount * 12)
		reasons = append(reasons, "provider_sources")
	}

	matchedTitle := ""
	if strings.TrimSpace(hints.Title) != "" {
		titleSimilarity, title := bestCandidateTitleSimilarity(hints.Title, candidate, hints.Year)
		matchedTitle = title
		if titleSimilarity == 1 {
			score += 45
			reasons = append(reasons, "exact_title")
		} else {
			score += titleSimilarity * 35
			if titleSimilarity > 0 {
				reasons = append(reasons, "coherent_title")
			}
		}
	}

	switch {
	case hints.Year != 0 && candidate.Year == hints.Year:
		score += 20
		reasons = append(reasons, "exact_year")
	case hints.Year != 0 && candidate.Year != 0 && math.Abs(float64(candidate.Year-hints.Year)) == 1:
		score += 5
	}

	if len(candidate.ProviderIDs) > 0 {
		score += 5
		score += float64(providerIDRichness(candidate.ProviderIDs))
	}

	return score, matchedTitle, reasons
}

func candidateTitles(candidate MatchCandidate) []string {
	titles := []string{candidate.Title, candidate.OriginalTitle}
	for _, alias := range candidate.TitleAliases {
		titles = append(titles, alias.Title)
	}
	return titles
}

func bestCandidateTitleSimilarity(hint string, candidate MatchCandidate, year int) (float64, string) {
	// A no-year local folder still has enough information to remove a provider's
	// own trailing year decoration. Without this fallback, a corroborated result
	// such as "Adventure Time (2010)" scores below an undecorated same-title
	// competitor before the no-year consensus tie-break can run.
	if year == 0 {
		year = candidate.Year
	}
	bestScore := 0.0
	bestTitle := ""
	for _, title := range candidateTitles(candidate) {
		score := inferTitleSimilarity(hint, title, year)
		if score > bestScore {
			bestScore = score
			bestTitle = title
		}
	}
	return bestScore, bestTitle
}

func annotateCandidateMatch(candidate *MatchCandidate, hints *MatchHints) {
	if candidate == nil {
		return
	}
	candidate.MatchScore, candidate.MatchedTitle, candidate.MatchReasons = scoreMatchCandidateDetailed(hints, *candidate)
	if len(candidate.ConflictingProviderIDKeys) > 0 {
		candidate.MatchReasons = append(candidate.MatchReasons, "provider_id_consensus")
		for _, key := range candidate.ConflictingProviderIDKeys {
			if strings.TrimSpace(candidate.ConfirmedProviderIDs[key]) != "" {
				candidate.MatchReasons = append(candidate.MatchReasons, "resolved_"+key+"_id")
			} else {
				candidate.MatchReasons = append(candidate.MatchReasons, "quarantined_"+key+"_id")
			}
		}
	}
}

type scoredMatchCandidate struct {
	candidate MatchCandidate
	score     float64
}

// candidatesAreSingleDistinctShow reports whether every scored candidate refers
// to the same show as best — same year, an exact normalized title match, and no
// conflicting provider IDs. This is true when the search effectively returned
// one distinct title, possibly as separate per-source rows (e.g. a TVDB row and
// a TMDB row that weren't merged because each carries only its own provider's
// ID). Candidates that share a canonical provider key but carry different values
// are considered distinct shows and cause the function to return false.
// Candidates with Year == 0 are treated as year-mismatched (a provider that
// omitted the year yields false here) — conservative by design.
func candidatesAreSingleDistinctShow(best MatchCandidate, scored []scoredMatchCandidate) bool {
	// Year==0 means the provider didn't supply a release year, so we cannot
	// claim the candidates refer to the *same* show via year-equality. Without
	// this guard, two no-year candidates from different providers would satisfy
	// the multi-source corroboration arm of the lone-result rule and get
	// auto-accepted, which over-accepts ambiguous matches.
	if best.Year == 0 {
		return false
	}
	// Track the first non-empty value seen per canonical provider key across
	// best AND every scored candidate. If any key ends up with more than one
	// distinct value, the tie group spans multiple shows — including the case
	// where `best` lacks a key but two non-best candidates carry conflicting
	// values for it.
	seenIDs := make(map[string]string, len(canonicalCandidateIDKeys))
	for _, key := range canonicalCandidateIDKeys {
		if v := strings.TrimSpace(best.ProviderIDs[key]); v != "" {
			seenIDs[key] = v
		}
	}
	for _, c := range scored {
		if c.candidate.Year == 0 || c.candidate.Year != best.Year {
			return false
		}
		if candidateToCandidateTitleSimilarity(best, c.candidate, best.Year) != 1 {
			return false
		}
		for _, key := range canonicalCandidateIDKeys {
			cv := strings.TrimSpace(c.candidate.ProviderIDs[key])
			if cv == "" {
				continue
			}
			if existing, ok := seenIDs[key]; ok {
				if existing != cv {
					return false
				}
			} else {
				seenIDs[key] = cv
			}
		}
	}
	return true
}

// topTieGroup returns the highest-scored candidate plus every candidate within
// the 15-point tie window of it — i.e. the set of candidates that are not
// clearly beaten. Assumes scored is sorted descending by score.
func topTieGroup(scored []scoredMatchCandidate) []scoredMatchCandidate {
	if len(scored) == 0 {
		return nil
	}
	group := []scoredMatchCandidate{scored[0]}
	for _, c := range scored[1:] {
		if scored[0].score-c.score < 15 {
			group = append(group, c)
		}
	}
	return group
}

// nonCorroboratingSources are provider slugs whose search results must never
// count as independent corroboration: a local sidecar (NFO) echoing a title
// is not a second provider agreeing on identity.
var nonCorroboratingSources = map[string]bool{"nfo": true}

// pickByProviderPriority returns the group candidate whose Sources include the
// highest-priority provider (providerPriority is ordered highest-first, e.g. the
// library's chain order). Falls back to the top-scored candidate when there is no
// priority info or no source matches.
//
// The group has already been proven to be one distinct show, so two defenses
// against ID-less local candidates apply: when any group member carries
// provider IDs, ID-less members are excluded from the priority pick (a
// title-only NFO first in the chain must not downgrade a remotely-found movie
// to an unenriched local item), and the winner adopts the union of the
// group's provider IDs (sound because candidatesAreSingleDistinctShow already
// verified they cannot conflict).
func pickByProviderPriority(group []scoredMatchCandidate, providerPriority []string) *MatchCandidate {
	eligible := group
	if anyCandidateHasProviderIDs(group) {
		eligible = make([]scoredMatchCandidate, 0, len(group))
		for _, c := range group {
			if providerIDRichness(c.candidate.ProviderIDs) > 0 {
				eligible = append(eligible, c)
			}
		}
	}

	winner := &eligible[0].candidate
	for _, prov := range providerPriority {
		for i := range eligible {
			for _, s := range eligible[i].candidate.Sources {
				if strings.EqualFold(s, prov) {
					winner = &eligible[i].candidate
					return adoptGroupProviderIDs(winner, group)
				}
			}
		}
	}
	return adoptGroupProviderIDs(winner, group)
}

func anyCandidateHasProviderIDs(group []scoredMatchCandidate) bool {
	for _, c := range group {
		if providerIDRichness(c.candidate.ProviderIDs) > 0 {
			return true
		}
	}
	return false
}

// adoptGroupProviderIDs returns a copy of winner carrying the union of the
// group's canonical provider IDs.
func adoptGroupProviderIDs(winner *MatchCandidate, group []scoredMatchCandidate) *MatchCandidate {
	adopted := *winner
	adopted.ProviderIDs = make(map[string]string, len(winner.ProviderIDs))
	adopted.ConfirmedProviderIDs = make(map[string]string)
	for k, v := range winner.ProviderIDs {
		adopted.ProviderIDs[k] = v
	}
	confirmedConflicts := make(map[string]bool)
	for _, c := range group {
		for _, key := range canonicalCandidateIDKeys {
			if v := strings.TrimSpace(c.candidate.ProviderIDs[key]); v != "" && adopted.ProviderIDs[key] == "" {
				adopted.ProviderIDs[key] = v
			}
		}
		for key, value := range c.candidate.ConfirmedProviderIDs {
			if confirmedConflicts[key] {
				continue
			}
			if existing := adopted.ConfirmedProviderIDs[key]; existing != "" && existing != value {
				delete(adopted.ConfirmedProviderIDs, key)
				confirmedConflicts[key] = true
				continue
			}
			adopted.ConfirmedProviderIDs[key] = value
		}
	}
	return &adopted
}

// distinctSourceCount returns how many distinct providers (case-insensitive
// Sources values) appear across the candidate group — i.e. how many independent
// providers returned this show. Non-corroborating local sources (nfo) never
// count.
func distinctSourceCount(group []scoredMatchCandidate) int {
	seen := make(map[string]struct{})
	for _, c := range group {
		for _, s := range c.candidate.Sources {
			s = strings.ToLower(strings.TrimSpace(s))
			if s != "" && !nonCorroboratingSources[s] {
				seen[s] = struct{}{}
			}
		}
	}
	return len(seen)
}

// absYearDelta returns the absolute difference between two release years.
func absYearDelta(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func selectInitialMatchCandidate(hints *MatchHints, candidates []MatchCandidate, providerPriority []string) (*MatchCandidate, bool) {
	if len(candidates) == 0 {
		return nil, false
	}

	scoredCandidates := make([]scoredMatchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		annotateCandidateMatch(&candidate, hints)
		scoredCandidates = append(scoredCandidates, scoredMatchCandidate{
			candidate: candidate,
			score:     candidate.MatchScore,
		})
	}
	sort.SliceStable(scoredCandidates, func(i, j int) bool {
		return scoredCandidates[i].score > scoredCandidates[j].score
	})

	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		// hints can be nil on some callers (scoreMatchCandidate tolerates it).
		// Guard the debug log so DEBUG-enabled runs don't panic on a nil deref.
		hintTitle, hintType := "", ""
		hintYear := 0
		if hints != nil {
			hintTitle = hints.Title
			hintYear = hints.Year
			hintType = hints.Type
		}
		for rank, sc := range scoredCandidates {
			slog.Debug("match candidate scoring",
				"hint_title", hintTitle,
				"hint_year", hintYear,
				"hint_type", hintType,
				"rank", rank,
				"candidate_title", sc.candidate.Title,
				"candidate_year", sc.candidate.Year,
				"candidate_type", sc.candidate.ContentType,
				"sources", strings.Join(sc.candidate.Sources, ","),
				"provider_ids", fmt.Sprintf("%v", sc.candidate.ProviderIDs),
				"score", sc.score,
			)
		}
	}

	if trustedHintIDsPresent(hints) {
		// Provider searches can return noisy title-ranked results even for an ID
		// query. A trusted local ID is decisive, so choose the highest-scored
		// candidate that actually carries the matching ID instead of requiring
		// the provider's first result to be correct.
		for i := range scoredCandidates {
			if candidateMatchesTrustedIDs(hints, scoredCandidates[i].candidate) {
				return &scoredCandidates[i].candidate, true
			}
		}
		return nil, false
	}

	// A title-only local sidecar is useful metadata, but it is not an
	// independent search result. Once a remote candidate with canonical IDs is
	// available, remove ID-less candidates from the automatic selection set so
	// their presence cannot turn the remote result into a misleading
	// multi-candidate score-gap win. They remain in the original candidate list
	// for diagnostics.
	if anyCandidateHasProviderIDs(scoredCandidates) {
		eligible := make([]scoredMatchCandidate, 0, len(scoredCandidates))
		for _, scored := range scoredCandidates {
			if providerIDRichness(scored.candidate.ProviderIDs) > 0 {
				eligible = append(eligible, scored)
			}
		}
		scoredCandidates = eligible
	}
	if len(scoredCandidates) == 0 {
		return nil, false
	}

	best := scoredCandidates[0]
	if best.score < automaticMatchAcceptanceFloor {
		return nil, false
	}
	// A known local year that conflicts with the provider by more than the
	// tolerated release-date window is negative evidence, not something a high
	// source-count score may erase. Trusted external IDs were handled above and
	// remain decisive; title-only matches must respect this guard even when two
	// providers return the same canonical item.
	if hints != nil && hints.Year != 0 && best.candidate.Year != 0 &&
		absYearDelta(best.candidate.Year, hints.Year) > 2 {
		return nil, false
	}
	// A search that resolves to a single distinct show (one candidate, or the
	// same title+year returned once per source) whose year matches the parsed
	// year is high-confidence even when the fuzzy title score sits in the 55-69
	// band (short/numeric/alternate titles). Accept the top-ranked candidate
	// without lowering the score thresholds.
	// We check only the TOP tie-group (candidates within 15 pts of best) so that
	// low-score noise from unrelated shows below the group does not veto a clear
	// cross-source agreement. When the top group is one distinct show, pick the
	// winner by the library's metadata-provider priority (falls back to top-scored).
	// Residual risk: two different shows with an identical title+year and no
	// provider IDs would both pass; accepted as low-risk given the title+year+type
	// corroboration.
	if candidateTypeMatchesHint(hints.Type, best.candidate.ContentType) {
		topGroup := topTieGroup(scoredCandidates)
		if candidatesAreSingleDistinctShow(best.candidate, topGroup) {
			yearCorroborated := hints.Year != 0 && best.candidate.Year == hints.Year
			// Cross-source agreement (the same title+year returned by 2+ distinct
			// providers, which candidatesAreSingleDistinctShow already verified) is
			// strong independent corroboration — it stands in for a missing hint year
			// (folders without a "(YYYY)"). A lone single-source no-year result is NOT
			// accepted here and stays subject to the single-candidate >=70 gate.
			multiSourceCorroborated := hints.Year == 0 && distinctSourceCount(topGroup) >= 2
			// An exact normalized-title match on a sole distinct show is strong
			// corroboration on its own, even when the folder year is off by a year
			// or two (festival vs wide-release date, regional release) — e.g.
			// "Dead Reckoning (1947)" vs TMDB's 1946, "17 Blocks (2021)" vs 2019.
			// Bounded to ±2 years so same-title remakes decades apart still require
			// a year or multi-source match. Uses the same normalizer as title scoring
			// so "exact" here means a perfect title-similarity component.
			titleSimilarity, _ := bestCandidateTitleSimilarity(hints.Title, best.candidate, hints.Year)
			titleCorroborated := hints.Year != 0 && best.candidate.Year != 0 &&
				absYearDelta(best.candidate.Year, hints.Year) <= 2 &&
				titleSimilarity == 1
			// Year or source-count corroboration is only meaningful when the winning
			// candidate is at least title-coherent with the scanner hint. Otherwise a
			// high source/provider score can auto-accept an unrelated same-year result.
			hintTitleCoherent := titleSimilarity > 0
			if (hintTitleCoherent && (yearCorroborated || multiSourceCorroborated)) || titleCorroborated {
				return pickByProviderPriority(topGroup, providerPriority), true
			}
		}
	}
	if len(scoredCandidates) == 1 {
		if best.score < 70 {
			return nil, false
		}
		return &best.candidate, true
	}
	if best.score-scoredCandidates[1].score < 15 {
		if winner, ok := tmdbTVDBTitleConsensusWinner(hints, scoredCandidates); ok {
			return winner, true
		}
		if winner, ok := duplicateTieBreakWinner(hints, scoredCandidates); ok {
			return winner, true
		}
		return providerOrderExactTieBreakWinner(hints, scoredCandidates)
	}
	return &best.candidate, true
}

// tmdbTVDBTitleConsensusWinner resolves a narrow series-only ambiguity when
// the local folder omits a year. It accepts only a candidate tied for the
// existing highest score, and only when exactly one candidate has an exact
// title match, a provider year, and independently retained TMDB and TVDB
// identities. This deliberately does not merge any metadata or IDs from
// competing candidates.
func tmdbTVDBTitleConsensusWinner(hints *MatchHints, scoredCandidates []scoredMatchCandidate) (*MatchCandidate, bool) {
	if hints == nil || !strings.EqualFold(strings.TrimSpace(hints.Type), "series") || hints.Year != 0 || len(scoredCandidates) < 2 {
		return nil, false
	}

	qualifyingIndex := -1
	for i := range scoredCandidates {
		candidate := scoredCandidates[i].candidate
		if !strings.EqualFold(strings.TrimSpace(candidate.ContentType), "series") || candidate.Year == 0 {
			continue
		}
		if similarity, _ := bestCandidateTitleSimilarity(hints.Title, candidate, 0); similarity != 1 {
			continue
		}
		if !candidateHasSource(candidate, "tmdb") || !candidateHasSource(candidate, "tvdb") {
			continue
		}
		if !candidateRetainsCanonicalProviderID(candidate, "tmdb") || !candidateRetainsCanonicalProviderID(candidate, "tvdb") {
			continue
		}
		if qualifyingIndex != -1 {
			return nil, false
		}
		qualifyingIndex = i
	}

	if qualifyingIndex == -1 || scoredCandidates[qualifyingIndex].score != scoredCandidates[0].score {
		return nil, false
	}
	winner := scoredCandidates[qualifyingIndex].candidate
	if !slices.Contains(winner.MatchReasons, "tmdb_tvdb_title_consensus") {
		winner.MatchReasons = append(winner.MatchReasons, "tmdb_tvdb_title_consensus")
	}
	return &winner, true
}

func candidateRetainsCanonicalProviderID(candidate MatchCandidate, provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, conflictingKey := range candidate.ConflictingProviderIDKeys {
		if strings.EqualFold(strings.TrimSpace(conflictingKey), provider) {
			return strings.TrimSpace(candidate.ConfirmedProviderIDs[provider]) != ""
		}
	}
	return strings.TrimSpace(candidate.ProviderIDs[provider]) != ""
}

func candidateHasSource(candidate MatchCandidate, provider string) bool {
	for _, source := range candidate.Sources {
		if strings.EqualFold(strings.TrimSpace(source), provider) {
			return true
		}
	}
	return false
}

func providerOrderExactTieBreakWinner(hints *MatchHints, scoredCandidates []scoredMatchCandidate) (*MatchCandidate, bool) {
	if hints == nil || len(scoredCandidates) < 2 {
		return nil, false
	}

	best := scoredCandidates[0]
	contenders := []scoredMatchCandidate{best}
	for i := 1; i < len(scoredCandidates); i++ {
		next := scoredCandidates[i]
		if best.score-next.score >= 15 {
			break
		}
		contenders = append(contenders, next)
	}
	if len(contenders) < 2 {
		return nil, false
	}

	seenPrimaryProviders := make(map[string]struct{}, len(contenders))
	for _, contender := range contenders {
		if !exactTitleYearTypeMatch(hints, contender.candidate) {
			return nil, false
		}

		primaryProvider := candidatePrimaryProvider(contender.candidate)
		if primaryProvider == "" {
			return nil, false
		}
		if _, exists := seenPrimaryProviders[primaryProvider]; exists {
			return nil, false
		}
		seenPrimaryProviders[primaryProvider] = struct{}{}
	}

	return &best.candidate, true
}

func exactTitleYearTypeMatch(hints *MatchHints, candidate MatchCandidate) bool {
	if hints == nil || hints.Year == 0 || candidate.Year == 0 {
		return false
	}
	if candidate.Year != hints.Year {
		return false
	}
	if !candidateTypeMatchesHint(hints.Type, candidate.ContentType) {
		return false
	}
	similarity, _ := bestCandidateTitleSimilarity(hints.Title, candidate, hints.Year)
	return similarity == 1
}

func candidateToCandidateTitleSimilarity(left, right MatchCandidate, year int) float64 {
	best := 0.0
	for _, leftTitle := range candidateTitles(left) {
		for _, rightTitle := range candidateTitles(right) {
			if similarity := inferTitleSimilarity(leftTitle, rightTitle, year); similarity > best {
				best = similarity
			}
		}
	}
	return best
}

func candidatePrimaryProvider(candidate MatchCandidate) string {
	for _, key := range canonicalCandidateIDKeys {
		if strings.TrimSpace(candidate.ProviderIDs[key]) != "" {
			return key
		}
	}
	if len(candidate.Sources) == 1 {
		return strings.TrimSpace(candidate.Sources[0])
	}
	return ""
}

// selectRefreshMatchCandidate picks the refresh winner anchored on the
// existing item's identity. idOverrides carries identity-hint values that won
// the pre-search conflict policy (e.g. NFO <uniqueid> on a manual refresh)
// and takes precedence over the stored ids for anchoring.
func selectRefreshMatchCandidate(existing *models.MediaItem, idOverrides map[string]string, candidates []MatchCandidate) (*MatchCandidate, bool) {
	if existing == nil || len(candidates) == 0 {
		return nil, false
	}

	hints := &MatchHints{
		Title:  existing.Title,
		Year:   existing.Year,
		Type:   existing.Type,
		TmdbID: existing.TmdbID,
		TvdbID: existing.TvdbID,
		ImdbID: existing.ImdbID,
	}
	overrideHintIDs(hints, idOverrides)
	return selectInitialMatchCandidate(hints, candidates, nil)
}

func trustedHintIDsPresent(hints *MatchHints) bool {
	for _, key := range trustedSearchIDKeys {
		if trustedIDValue(hints, key) != "" {
			return true
		}
	}
	return false
}

func candidatesConflictWithTrustedIDs(hints *MatchHints, candidates []MatchCandidate) bool {
	if hints == nil {
		return false
	}
	for _, candidate := range candidates {
		for _, key := range trustedSearchIDKeys {
			hintValue := strings.TrimSpace(trustedIDValue(hints, key))
			candidateValue := strings.TrimSpace(candidate.ProviderIDs[key])
			if hintValue != "" && candidateValue != "" && hintValue != candidateValue {
				return true
			}
		}
	}
	return false
}

func candidateMatchesTrustedIDs(hints *MatchHints, candidate MatchCandidate) bool {
	matched := false
	for _, key := range trustedSearchIDKeys {
		hintValue := trustedIDValue(hints, key)
		if hintValue == "" {
			continue
		}
		candidateValue := candidate.ProviderIDs[key]
		if candidateValue == "" {
			continue
		}
		if candidateValue != hintValue {
			return false
		}
		matched = true
	}
	return matched
}

func trustedIDValue(hints *MatchHints, key string) string {
	if hints == nil {
		return ""
	}
	switch key {
	case "metadb":
		return hints.ContentID
	case "tmdb":
		return hints.TmdbID
	case "tvdb":
		return hints.TvdbID
	case "imdb":
		return hints.ImdbID
	default:
		return ""
	}
}

func normalizeCandidateTitle(title string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
}

func inferTitleSimilarity(left, right string, year int) float64 {
	leftNorm := normalizeCandidateTitleForYear(left, year)
	rightNorm := normalizeCandidateTitleForYear(right, year)
	if leftNorm == "" || rightNorm == "" {
		return 0
	}
	if leftNorm == rightNorm {
		return 1
	}
	leftComparable := strings.Join(strings.Fields(normalizeTitleForScoring(leftNorm)), " ")
	rightComparable := strings.Join(strings.Fields(normalizeTitleForScoring(rightNorm)), " ")
	if leftComparable == rightComparable {
		return 1
	}
	if naming.InferTitlesCoherent(left, right) {
		return 0.8
	}
	return 0
}

func normalizeCandidateTitleForYear(title string, year int) string {
	normalized := normalizeCandidateTitle(title)
	if normalized == "" || year == 0 {
		return normalized
	}
	yearText := strconv.Itoa(year)
	fields := strings.Fields(normalized)
	if len(fields) <= 1 {
		return normalized
	}
	// Providers frequently disambiguate a reused title by appending the release
	// year to the title string itself ("24 stjerners julikalender (DK) (2026)").
	// The scanner strips "(YYYY)" from the folder name into a separate year, so
	// without this the two sides can never compare equal. Only the known year is
	// stripped, so an unrelated trailing number stays part of the title.
	last := fields[len(fields)-1]
	if last != yearText && trimYearDecoration(last) != yearText {
		return normalized
	}
	return strings.Join(fields[:len(fields)-1], " ")
}

// trimYearDecoration unwraps a bracketed year token — "(2026)", "[2026]" — to
// its bare digits. Returns the input unchanged when it is not so wrapped.
func trimYearDecoration(token string) string {
	for _, pair := range [][2]string{{"(", ")"}, {"[", "]"}} {
		if inner, ok := strings.CutPrefix(token, pair[0]); ok {
			if inner, ok := strings.CutSuffix(inner, pair[1]); ok {
				return inner
			}
		}
	}
	return token
}

// nonCombiningLetterFolds covers letters that are not decomposable combining
// forms, so NFD alone leaves them untouched. European library folders routinely
// carry the ASCII spelling while providers return these.
var nonCombiningLetterFolds = strings.NewReplacer(
	"ø", "o", "Ø", "o",
	"æ", "ae", "Æ", "ae",
	"œ", "oe", "Œ", "oe",
	"ß", "ss", "ẞ", "ss",
	"đ", "d", "Đ", "d",
	"ł", "l", "Ł", "l",
	"ı", "i",
)

// foldTitleDiacritics strips Latin combining marks so provider spellings that
// differ only by accents compare equal ("Yeşilçam" vs "Yesilcam"). Combining
// marks remain significant in other scripts, where they can change lexical
// identity rather than decorate an otherwise equivalent Latin letter.
// Deliberately avoids a shared chained transformer: transformers carry per-call
// state, and this runs concurrently across episode-validation workers.
func foldTitleDiacritics(title string) string {
	if isASCIIOnly(title) {
		return title
	}
	title = nonCombiningLetterFolds.Replace(title)
	if isASCIIOnly(title) {
		return title
	}
	decomposed := norm.NFD.String(title)
	var builder strings.Builder
	builder.Grow(len(decomposed))
	var lastStarter rune
	for _, r := range decomposed {
		// Spacing kana marks otherwise fall through punctuation cleanup; convert
		// them so NFC can recompose the voiced kana before title tokenization.
		switch r {
		case '\u309b':
			r = '\u3099'
		case '\u309c':
			r = '\u309a'
		}
		if unicode.Is(unicode.Mn, r) && unicode.Is(unicode.Latin, lastStarter) {
			continue
		}
		builder.WriteRune(r)
		if !unicode.Is(unicode.Mn, r) {
			lastStarter = r
		}
	}
	return norm.NFC.String(builder.String())
}

func isASCIIOnly(value string) bool {
	for i := range len(value) {
		if value[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func normalizeTitleForScoring(title string) string {
	title = naming.StripComparisonSafeEditionSuffix(title)
	title = foldTitleDiacritics(title)
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(title))
	lastComparableWasAlnum := false
	for _, r := range title {
		if digit, ok := normalizeNumericRune(r); ok {
			if isStyledNumericRune(r) && lastComparableWasAlnum {
				builder.WriteByte(' ')
			}
			builder.WriteRune(digit)
			lastComparableWasAlnum = true
			continue
		}

		switch {
		case unicode.IsLetter(r):
			builder.WriteRune(r)
			lastComparableWasAlnum = true
		case r == '&':
			builder.WriteString(" and ")
			lastComparableWasAlnum = true
		case r == '\'':
			// Collapse contractions like "what's" -> "whats" so scanner- and
			// provider-derived variants can compare as exact.
		default:
			builder.WriteByte(' ')
			lastComparableWasAlnum = false
		}
	}

	fields := strings.Fields(builder.String())
	for i, field := range fields {
		fields[i] = normalizeNumberWord(field)
	}
	return strings.Join(fields, " ")
}

var normalizedNumberWords = map[string]string{
	"zero": "0", "zeroth": "0",
	//nolint:goconst // This is a declarative number/ordinal lookup, not a domain label.
	"one": "1", "first": "1",
	"two": "2", "second": "2", //nolint:goconst // Ordinal word mapping, not a reusable domain value.
	"three": "3", "third": "3",
	"four": "4", "fourth": "4",
	"five": "5", "fifth": "5",
	"six": "6", "sixth": "6",
	"seven": "7", "seventh": "7",
	"eight": "8", "eighth": "8",
	"nine": "9", "ninth": "9",
	"ten": "10", "tenth": "10",
	"eleven": "11", "eleventh": "11",
	"twelve": "12", "twelfth": "12",
	"thirteen": "13", "thirteenth": "13",
	"fourteen": "14", "fourteenth": "14",
	"fifteen": "15", "fifteenth": "15",
	"sixteen": "16", "sixteenth": "16",
	"seventeen": "17", "seventeenth": "17",
	"eighteen": "18", "eighteenth": "18",
	"nineteen": "19", "nineteenth": "19",
	"twenty": "20", "twentieth": "20",
}

func normalizeNumberWord(value string) string {
	if normalized, ok := normalizedNumberWords[value]; ok {
		return normalized
	}
	for _, suffix := range []string{"st", "nd", "rd", "th"} {
		if stem, ok := strings.CutSuffix(value, suffix); ok && isASCIIDigits(stem) {
			return stem
		}
	}
	return value
}

func isASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizeNumericRune(r rune) (rune, bool) {
	switch r {
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return r, true
	case '⁰', '₀':
		return '0', true
	case '¹', '₁':
		return '1', true
	case '²', '₂':
		return '2', true
	case '³', '₃':
		return '3', true
	case '⁴', '₄':
		return '4', true
	case '⁵', '₅':
		return '5', true
	case '⁶', '₆':
		return '6', true
	case '⁷', '₇':
		return '7', true
	case '⁸', '₈':
		return '8', true
	case '⁹', '₉':
		return '9', true
	default:
		return 0, false
	}
}

func isStyledNumericRune(r rune) bool {
	switch r {
	case '⁰', '¹', '²', '³', '⁴', '⁵', '⁶', '⁷', '⁸', '⁹', '₀', '₁', '₂', '₃', '₄', '₅', '₆', '₇', '₈', '₉':
		return true
	default:
		return false
	}
}

package metadata

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// MatchCandidate represents a deduplicated search result grouped by normalized
// provider IDs. Multiple raw SearchResult rows from different providers that
// share the same TMDB/TVDB/IMDB IDs are collapsed into a single candidate.
type MatchCandidate struct {
	Title          string            `json:"title"`
	Year           int               `json:"year"`
	ContentType    string            `json:"content_type"`
	ProviderIDs    map[string]string `json:"provider_ids"`
	ImageURL       string            `json:"image_url,omitempty"`
	Overview       string            `json:"overview,omitempty"`
	Sources        []string          `json:"sources"`
	AgreementHints []string          `json:"agreement_hints"`
	DetailScore    int               `json:"-"`
}

var canonicalCandidateIDKeys = []string{"tmdb", "tvdb", "imdb"}

func compatibleProviderIDs(left, right map[string]string) bool {
	overlap := false
	for _, key := range canonicalCandidateIDKeys {
		lv := strings.TrimSpace(left[key])
		rv := strings.TrimSpace(right[key])
		if lv == "" || rv == "" {
			continue
		}
		if lv != rv {
			return false
		}
		overlap = true
	}
	return overlap
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

const (
	minimumDetailTieBreakScore = 20
	minimumDetailTieBreakGap   = 12
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
	if inferTitleSimilarity(left.Title, right.Title, hints.Year) != 1 {
		return false
	}
	if inferTitleSimilarity(hints.Title, left.Title, hints.Year) != 1 {
		return false
	}
	if inferTitleSimilarity(hints.Title, right.Title, hints.Year) != 1 {
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
	type bucket struct {
		candidate MatchCandidate
		sources   map[string]bool
	}

	ordered := make([]string, 0)
	buckets := make(map[string]*bucket)

	for _, sr := range results {
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
			b = &bucket{
				candidate: MatchCandidate{
					Title:       sr.Name,
					Year:        sr.Year,
					ContentType: contentType,
					ProviderIDs: make(map[string]string),
					ImageURL:    sr.ImageURL,
					Overview:    sr.Overview,
				},
				sources: make(map[string]bool),
			}
			buckets[key] = b
			ordered = append(ordered, key)
		}

		// Merge provider IDs.
		for k, v := range sr.ProviderIDs {
			if v != "" {
				b.candidate.ProviderIDs[k] = v
			}
		}

		// Track source providers.
		if sr.Provider != "" {
			b.sources[sr.Provider] = true
		}

		// Prefer non-empty overview and image.
		if b.candidate.Overview == "" && sr.Overview != "" {
			b.candidate.Overview = sr.Overview
		}
		if b.candidate.ImageURL == "" && sr.ImageURL != "" {
			b.candidate.ImageURL = sr.ImageURL
		}
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

		// Compute agreement hints.
		if len(sources) > 1 {
			b.candidate.AgreementHints = append(b.candidate.AgreementHints,
				"agreed_by_"+strings.Join(sources, "_and_"))
		}

		candidates = append(candidates, b.candidate)
	}

	return candidates
}

// SearchAndNormalize is a convenience method that calls SearchProviders and
// normalizes the results into MatchCandidates. Plugin-prefixed image URLs
// (e.g. "metadb://...") are resolved to presigned HTTP URLs before returning.
func (s *MetadataService) SearchAndNormalize(ctx context.Context, query SearchQuery, folderID int) ([]MatchCandidate, error) {
	results, err := s.SearchProviders(ctx, query, folderID)
	if err != nil {
		return nil, err
	}
	candidates := NormalizeCandidates(results, query.ContentType)

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
	if hints == nil {
		return 0
	}

	score := 0.0
	trustedIDMatches := 0
	for _, key := range trustedSearchIDKeys {
		hintValue := trustedIDValue(hints, key)
		if hintValue == "" {
			continue
		}
		if candidate.ProviderIDs[key] == hintValue {
			score += 100
			trustedIDMatches++
		}
	}
	if trustedIDMatches > 0 {
		score += float64(trustedIDMatches * 10)
	}

	score += float64(len(candidate.Sources) * 12)

	if strings.TrimSpace(hints.Title) != "" && strings.TrimSpace(candidate.Title) != "" {
		titleSimilarity := inferTitleSimilarity(hints.Title, candidate.Title, hints.Year)
		if titleSimilarity == 1 {
			score += 45
		} else {
			score += titleSimilarity * 35
		}
	}

	switch {
	case hints.Year != 0 && candidate.Year == hints.Year:
		score += 20
	case hints.Year != 0 && candidate.Year != 0 && math.Abs(float64(candidate.Year-hints.Year)) == 1:
		score += 5
	}

	if len(candidate.ProviderIDs) > 0 {
		score += 5
		score += float64(providerIDRichness(candidate.ProviderIDs))
	}

	return score
}

type scoredMatchCandidate struct {
	candidate MatchCandidate
	score     float64
}

func selectInitialMatchCandidate(hints *MatchHints, candidates []MatchCandidate) (*MatchCandidate, bool) {
	if len(candidates) == 0 {
		return nil, false
	}

	scoredCandidates := make([]scoredMatchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		scoredCandidates = append(scoredCandidates, scoredMatchCandidate{
			candidate: candidate,
			score:     scoreMatchCandidate(hints, candidate),
		})
	}
	sort.SliceStable(scoredCandidates, func(i, j int) bool {
		return scoredCandidates[i].score > scoredCandidates[j].score
	})

	best := scoredCandidates[0]
	if trustedHintIDsPresent(hints) {
		if candidateMatchesTrustedIDs(hints, best.candidate) {
			return &best.candidate, true
		}
		return nil, false
	}

	if best.score < 55 {
		return nil, false
	}
	if len(scoredCandidates) == 1 {
		if best.score < 70 {
			return nil, false
		}
		return &best.candidate, true
	}
	if best.score-scoredCandidates[1].score < 15 {
		return duplicateTieBreakWinner(hints, scoredCandidates)
	}
	return &best.candidate, true
}

func selectRefreshMatchCandidate(existing *models.MediaItem, candidates []MatchCandidate) (*MatchCandidate, bool) {
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
	return selectInitialMatchCandidate(hints, candidates)
}

func trustedHintIDsPresent(hints *MatchHints) bool {
	for _, key := range trustedSearchIDKeys {
		if trustedIDValue(hints, key) != "" {
			return true
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
	if len(fields) <= 1 || fields[len(fields)-1] != yearText {
		return normalized
	}
	return strings.Join(fields[:len(fields)-1], " ")
}

func normalizeTitleForScoring(title string) string {
	title = naming.StripComparisonSafeEditionSuffix(title)
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

	return strings.Join(strings.Fields(builder.String()), " ")
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

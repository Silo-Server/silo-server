package naming

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	inferBracketTitleYearRe = regexp.MustCompile(`^(.+?)\s*[\(\[](\d{4})[\)\]]`)
)

type inferMovieStem struct {
	Title      string
	Year       int
	Remainder  string
	Confidence string
}

type InferMovieStem = inferMovieStem

func ParseInferMovieStem(name string, folderTitle string, folderYear int) InferMovieStem {
	return parseInferMovieStem(name, folderTitle, folderYear)
}

func ParseInferFolderTitleYear(name string) (string, int, bool) {
	return parseInferFolderTitleYear(name)
}

func InferTitlesCoherent(left string, right string) bool {
	return inferTitlesCoherent(left, right)
}

func parseInferMovieStem(name string, folderTitle string, folderYear int) inferMovieStem {
	surface := stripInferProviderTags(name)
	surface = strings.NewReplacer(".", " ", "_", " ").Replace(surface)
	surface = collapseWhitespace(strings.TrimSpace(surface))
	if surface == "" {
		return inferMovieStem{}
	}

	folderTokens := normalizeInferTokens(folderTitle)
	tokens := strings.Fields(surface)
	if len(tokens) == 0 {
		return inferMovieStem{}
	}

	bestIdx := -1
	bestScore := -1
	bestTitle := ""
	bestRemainder := ""

	for idx, token := range tokens {
		year, ok := parseInferYearToken(token)
		if !ok {
			continue
		}
		titleTokens := tokens[:idx]
		if len(titleTokens) == 0 {
			continue
		}
		remainderTokens := tokens[idx+1:]
		if !inferHasSuffixEvidence(remainderTokens) && len(remainderTokens) > 0 {
			continue
		}

		titleCandidate := collapseWhitespace(strings.Join(titleTokens, " "))
		score := inferMovieStemScore(titleTokens, remainderTokens, folderTokens, folderYear, year)
		if score > bestScore || (score == bestScore && idx > bestIdx) {
			bestIdx = idx
			bestScore = score
			bestTitle = titleCandidate
			bestRemainder = collapseWhitespace(strings.Join(remainderTokens, " "))
		}
	}

	if bestIdx < 0 {
		return inferMovieStem{}
	}

	confidence := "medium"
	if bestScore >= 6 {
		confidence = "high"
	} else if len(normalizeInferTokens(bestTitle)) <= 1 && bestRemainder == "" {
		confidence = "low"
	}

	year, _ := parseInferYearToken(tokens[bestIdx])
	return inferMovieStem{
		Title:      bestTitle,
		Year:       year,
		Remainder:  bestRemainder,
		Confidence: confidence,
	}
}

func inferMovieStemScore(titleTokens []string, remainderTokens []string, folderTokens []string, folderYear int, year int) int {
	score := 0
	switch {
	case len(remainderTokens) == 0:
		score += 1
	case inferHasSuffixEvidence(remainderTokens):
		score += 4
	default:
		score += 1
	}

	if len(folderTokens) > 0 {
		similarity := inferTokenSimilarity(titleTokens, folderTokens)
		switch {
		case similarity >= 0.999:
			score += 6
		case similarity >= 0.85:
			score += 4
		case similarity >= 0.6:
			score += 1
		}
		if folderYear != 0 && folderYear == year {
			score += 2
		}
	}

	return score
}

func inferHasSuffixEvidence(tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	for i, token := range tokens {
		if inferEditionTokenKey(token) != "" {
			return true
		}
		if inferReleaseTokenRe.MatchString(token) || inferLooksLikeReleaseGroup(strings.Join(tokens[i:], " ")) {
			return true
		}
	}
	return false
}

func inferLooksLikeReleaseGroup(surface string) bool {
	trimmed := strings.TrimSpace(surface)
	if trimmed == "" {
		return false
	}
	return stripVariantReleaseGroup(trimmed) != trimmed
}

func parseInferFolderTitleYear(name string) (string, int, bool) {
	surface := stripInferProviderTags(name)
	surface = collapseWhitespace(strings.TrimSpace(surface))
	if surface == "" {
		return "", 0, false
	}
	if match := inferBracketTitleYearRe.FindStringSubmatch(surface); match != nil {
		year, _ := strconv.Atoi(match[2])
		return strings.TrimSpace(match[1]), year, true
	}
	if ParseFolderIDs(name) != nil {
		return strings.TrimSpace(surface), 0, true
	}
	return strings.TrimSpace(surface), 0, false
}

func normalizeInferComparable(name string) string {
	return strings.Join(normalizeInferTokens(name), " ")
}

func normalizeInferTokens(name string) []string {
	surface := stripInferProviderTags(name)
	surface = strings.ToLower(surface)
	var builder strings.Builder
	builder.Grow(len(surface))
	lastComparableWasAlnum := false
	for _, r := range surface {
		if digit, ok := normalizeInferNumericRune(r); ok {
			if isStyledInferNumericRune(r) && lastComparableWasAlnum {
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
			// Keep contractions and possessives together: "what's" -> "whats".
		default:
			builder.WriteByte(' ')
			lastComparableWasAlnum = false
		}
	}
	fields := strings.Fields(builder.String())
	if len(fields) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(fields))
	for _, field := range fields {
		switch field {
		case "the", "a", "an":
			normalized = append(normalized, field)
		default:
			normalized = append(normalized, normalizeInferOrdinalToken(field))
		}
	}

	for len(normalized) > 1 && inferIsArticle(normalized[0]) {
		normalized = normalized[1:]
	}
	for len(normalized) > 1 && inferIsArticle(normalized[len(normalized)-1]) {
		normalized = normalized[:len(normalized)-1]
	}

	return normalized
}

func normalizeInferNumericRune(r rune) (rune, bool) {
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

func isStyledInferNumericRune(r rune) bool {
	switch r {
	case '⁰', '¹', '²', '³', '⁴', '⁵', '⁶', '⁷', '⁸', '⁹', '₀', '₁', '₂', '₃', '₄', '₅', '₆', '₇', '₈', '₉':
		return true
	default:
		return false
	}
}

func normalizeInferOrdinalToken(token string) string {
	switch token {
	case "i", "one", "first":
		return "1"
	case "ii", "two", "second":
		return "2"
	case "iii", "three", "third":
		return "3"
	case "iv", "four", "fourth":
		return "4"
	case "v", "five", "fifth":
		return "5"
	default:
		return token
	}
}

func inferIsArticle(token string) bool {
	switch token {
	case "the", "a", "an":
		return true
	default:
		return false
	}
}

func inferTokenSimilarity(left []string, right []string) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	leftSet := make(map[string]struct{}, len(left))
	rightSet := make(map[string]struct{}, len(right))
	for _, token := range left {
		if token != "" {
			leftSet[token] = struct{}{}
		}
	}
	for _, token := range right {
		if token != "" {
			rightSet[token] = struct{}{}
		}
	}
	if len(leftSet) == 0 || len(rightSet) == 0 {
		return 0
	}
	intersection := 0
	union := len(leftSet)
	for token := range rightSet {
		if _, ok := leftSet[token]; ok {
			intersection++
			continue
		}
		union++
	}
	return float64(intersection) / float64(union)
}

func inferTitlesCoherent(left string, right string) bool {
	leftTokens := normalizeInferTokens(StripComparisonSafeEditionSuffix(left))
	rightTokens := normalizeInferTokens(StripComparisonSafeEditionSuffix(right))
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return false
	}
	if strings.Join(leftTokens, " ") == strings.Join(rightTokens, " ") {
		return true
	}
	return inferTokenSimilarity(leftTokens, rightTokens) >= 0.85
}

func parseInferYearToken(token string) (int, bool) {
	if len(token) == 6 {
		if (token[0] == '(' && token[5] == ')') || (token[0] == '[' && token[5] == ']') {
			token = token[1:5]
		}
	}
	if len(token) != 4 {
		return 0, false
	}
	year, err := strconv.Atoi(token)
	if err != nil {
		return 0, false
	}
	if year < 1900 || year > 2099 {
		return 0, false
	}
	return year, true
}

package catalog

import (
	"strconv"
	"strings"
	"unicode"
)

type parsedSearchQuery struct {
	Raw            string
	Text           string
	Phrase         string
	ExactTitleHint string
	// NormalizedText is normalizeTitleForComparison(Text) computed once at parse
	// time. Text already folds phrase + remainder together, so this is the full
	// normalized query used by the fuzzy gate and leading-title lookup (which
	// would otherwise re-normalize on every sparse search).
	NormalizedText string
	Year           *int
}

func parseSearchQuery(raw string) parsedSearchQuery {
	trimmed := collapseSearchWhitespace(strings.TrimSpace(raw))
	phrase, remainder := extractBalancedPhrase(trimmed)
	year, remainder := extractYearHint(remainder, phrase != "")

	parts := make([]string, 0, 2)
	if phrase != "" {
		parts = append(parts, phrase)
	}
	if remainder != "" {
		parts = append(parts, remainder)
	}

	text := collapseSearchWhitespace(strings.Join(parts, " "))
	if text == "" {
		text = collapseSearchWhitespace(strings.ReplaceAll(trimmed, "\"", " "))
	}

	return parsedSearchQuery{
		Raw:            raw,
		Text:           text,
		Phrase:         phrase,
		ExactTitleHint: normalizeTitleForComparison(firstNonEmptySearchValue(phrase, text)),
		NormalizedText: normalizeTitleForComparison(text),
		Year:           year,
	}
}

// fuzzyMinTokenLen is the shortest normalized token that may enable the trigram
// fuzzy title fallback. A token shorter than this forms too few trigrams to use
// the gin_trgm_ops index selectively (and a 1-2 char token can't use it at
// all), so for short queries the fuzzy fallback is skipped and search stays on
// the exact FTS/prefix path. The gate is applied to the longest token so a stray
// short token ("a vengers") is judged on "vengers", not "a".
const fuzzyMinTokenLen = 4

// Single-token inputs shorter than this stay on the exact whole-title path.
// Even a non-prefix FTS lexeme such as "la" or "the" can match a large share
// of a multilingual catalog; ranking that set while the user is still typing
// is both wasteful and visibly slow. Multi-word input with a short final token
// uses the separate leading-title B-tree transition path below.
const searchPrefixMinTokenLen = 4

// eligibleForFuzzy reports whether a parsed query should use the trigram fuzzy
// title fallback. A quoted phrase with a short remainder stays exclusively on
// the indexed leading-title path so fuzzy matching cannot reintroduce results
// that satisfy only the phrase and omit the remainder.
func eligibleForFuzzy(parsed parsedSearchQuery) bool {
	if parsed.Phrase != "" && useLeadingShortTitleSearch(parsed) {
		return false
	}
	longest := 0
	for _, tok := range strings.Fields(parsed.NormalizedText) {
		if n := len([]rune(tok)); n > longest {
			longest = n
		}
	}
	return longest >= fuzzyMinTokenLen
}

func useExactShortTitleSearch(parsed parsedSearchQuery) bool {
	fields := strings.Fields(parsed.NormalizedText)
	return len(fields) == 1 && len([]rune(fields[0])) < searchPrefixMinTokenLen
}

// useLeadingShortTitleSearch handles the brief typeahead state where the user
// has completed at least one word but the final word is still too short for a
// selective catalog-wide lexeme expansion (for example, "the m"). A normalized
// leading-title B-tree lookup is deterministic and cheap during those few
// keystrokes; regular word-prefix FTS resumes at searchPrefixMinTokenLen.
func useLeadingShortTitleSearch(parsed parsedSearchQuery) bool {
	fields := strings.Fields(parsed.NormalizedText)
	return len(fields) > 1 && len([]rune(fields[len(fields)-1])) < searchPrefixMinTokenLen
}

func extractBalancedPhrase(input string) (string, string) {
	start := strings.Index(input, "\"")
	if start == -1 {
		return "", input
	}

	end := strings.Index(input[start+1:], "\"")
	if end == -1 {
		return "", collapseSearchWhitespace(strings.ReplaceAll(input, "\"", " "))
	}

	end += start + 1
	phrase := collapseSearchWhitespace(input[start+1 : end])
	remainder := collapseSearchWhitespace(strings.Join([]string{
		input[:start],
		input[end+1:],
	}, " "))
	return phrase, remainder
}

func extractYearHint(input string, hasPhrase bool) (*int, string) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil, ""
	}

	for i := len(fields) - 1; i >= 0; i-- {
		year, ok := parseYearToken(fields[i])
		if !ok {
			continue
		}
		if len(fields) == 1 && !hasPhrase {
			return nil, collapseSearchWhitespace(input)
		}

		remaining := append([]string{}, fields[:i]...)
		remaining = append(remaining, fields[i+1:]...)
		return &year, collapseSearchWhitespace(strings.Join(remaining, " "))
	}

	return nil, collapseSearchWhitespace(input)
}

func parseYearToken(token string) (int, bool) {
	if len(token) != 4 {
		return 0, false
	}
	year, err := strconv.Atoi(token)
	if err != nil {
		return 0, false
	}
	if year < 1900 || year > 2100 {
		return 0, false
	}
	return year, true
}

func buildTitlePrefixTsQuery(input string) string {
	normalized := normalizeTitleForComparison(input)
	if normalized == "" {
		return ""
	}

	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return ""
	}
	// A one- to three-character prefix over the whole catalog is not selective:
	// it can expand to a large fraction of episode titles and aliases while the
	// user is still typing. Keep exact FTS available for genuinely short titles,
	// but wait for four characters before enabling an unconstrained prefix.
	// Multi-word queries remain safe because their completed terms constrain the
	// final partial token (for example, "star w:*").
	if len(fields) == 1 && len([]rune(fields[0])) < searchPrefixMinTokenLen {
		return ""
	}

	parts := make([]string, 0, len(fields))
	for i, field := range fields {
		if field == "" {
			continue
		}
		if i == len(fields)-1 {
			parts = append(parts, field+":*")
			continue
		}
		parts = append(parts, field)
	}
	return strings.Join(parts, " & ")
}

// normalizeTitleForComparison must stay in lockstep with the SQL function
// public.normalize_search_text (migrations 127 / 138) and the title_normalized
// generated column. Mismatches between Go and SQL normalization produce
// asymmetric search results (Go-computed ExactTitleHint failing to match a
// row whose title_normalized has the same logical content).
func normalizeTitleForComparison(input string) string {
	var b strings.Builder
	b.Grow(len(input))

	for _, r := range input {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte(' ')
		}
	}

	return normalizeSearchTokens(collapseSearchWhitespace(b.String()))
}

// normalizeSearchTokens drops the standalone token "and" from a
// whitespace-separated lowercase string and maps common number words /
// ordinals to digit tokens. Together with the alphanumeric pass above, this
// mirrors public.normalize_search_text().
func normalizeSearchTokens(input string) string {
	if input == "" {
		return ""
	}
	fields := strings.Fields(input)
	filtered := fields[:0]
	for _, f := range fields {
		if f == "and" {
			continue
		}
		filtered = append(filtered, normalizeSearchNumberToken(f))
	}
	return strings.Join(filtered, " ")
}

func normalizeSearchNumberToken(token string) string {
	switch token {
	case "zero", "zeroth":
		return "0"
	case "one", "first":
		return "1"
	case "two", "second":
		return "2"
	case "three", "third":
		return "3"
	case "four", "fourth":
		return "4"
	case "five", "fifth":
		return "5"
	case "six", "sixth":
		return "6"
	case "seven", "seventh":
		return "7"
	case "eight", "eighth":
		return "8"
	case "nine", "ninth":
		return "9"
	case "ten", "tenth":
		return "10"
	case "eleven", "eleventh":
		return "11"
	case "twelve", "twelfth":
		return "12"
	case "thirteen", "thirteenth":
		return "13"
	case "fourteen", "fourteenth":
		return "14"
	case "fifteen", "fifteenth":
		return "15"
	case "sixteen", "sixteenth":
		return "16"
	case "seventeen", "seventeenth":
		return "17"
	case "eighteen", "eighteenth":
		return "18"
	case "nineteen", "nineteenth":
		return "19"
	case "twenty", "twentieth":
		return "20"
	}

	if stripped, ok := stripDigitOrdinalSuffix(token); ok {
		return stripped
	}
	return token
}

func stripDigitOrdinalSuffix(token string) (string, bool) {
	for _, suffix := range []string{"st", "nd", "rd", "th"} {
		stem := strings.TrimSuffix(token, suffix)
		if stem != token && hasOnlyDigits(stem) {
			return stem, true
		}
	}
	return "", false
}

func hasOnlyDigits(input string) bool {
	if input == "" {
		return false
	}
	for _, r := range input {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func collapseSearchWhitespace(input string) string {
	return strings.Join(strings.Fields(input), " ")
}

func firstNonEmptySearchValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

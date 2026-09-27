package naming

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const mediumIdentityConfidence = "medium"

var (
	inferBracketTitleYearRe = regexp.MustCompile(`^(.+?)\s*[\(\[](\d{4})[\)\]]`)
	nameSeparatorRe         = regexp.MustCompile(`\.+([^\s.])`)
	nameAcronymRe           = regexp.MustCompile(`\b(?:[A-Z]\.)+[A-Z]\b`)
	// Ordinary title words such as "Web" or "Extended" alone do not establish
	// a release suffix. Only technical terms delimit a yearless movie title.
	inferTechnicalSuffixRe = regexp.MustCompile(`(?i)(?:^|[ ._\-\[(])(?:[248]k|ultra[ ._-]?hd|uhd|hdr(?:10\+?)?|hdc|sdr|2160p|1080[pi]|720p|576[pi]|480[pi]|\d{3,4}x\d{3,4}|blu[ ._-]?ray|b[dr]rip|dvd[ ._-]?(?:rip|scr)|hdtv|web[ ._-]?(?:dl|rip)|hd[ ._-]?rip|remux|x26[45]|h[ .]?26[45]|hevc|avc|av1|xvid|divx|mpeg[ ._-]?[24]|aac(?:[ .]?\d[ .]?\d)?|e?ac[ ._-]?3|ddp?\d[ .]?\d|dts(?:[ ._-]?hd)?|truehd|flac|opus)(?:$|[^\p{L}\p{N}])`)
	inferYearBoundaryRe    = regexp.MustCompile(`(?:^|[^\p{L}\p{N}])[\(\[]?(?:19|20)\d{2}[\)\]]?(?:$|[^\p{L}\p{N}])`)
	inferTitleWordFormatRe = regexp.MustCompile(`(?i)^(?:[248]k|uhd|ultra[ ._-]?hd|hdr(?:10\+?)?|sdr|opus|flac|avc|blu[ ._-]?ray)$`)
	inferDiscTrackRe       = regexp.MustCompile(`(?i)^(?:(?:title\s*t?|t)\d+|vts\s*\d+\s*\d+)$`)
	inferMovieBracketRe    = regexp.MustCompile(`\[([^\[\]]+)\]`)
	inferMetadataBracketRe = regexp.MustCompile(`(?i)^(?:(?:multi(?:ple)?|dual)[ ._-]?(?:audio|subs?|subtitles?)?|[a-f0-9]{8}|字)$`)
	inferReleasePrefixRe   = regexp.MustCompile(`(?i)(?:subs|raws|encodes?|rips?|\.(?:com|net|org|mx|ag))$`)
	inferTerminalMultiRe   = regexp.MustCompile(`(?i)[ ._-]+multi$`)
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
	surface, bracketMetadata := cleanMovieIdentitySurface(name, folderTitle)
	if surface == "" {
		return inferMovieStem{}
	}
	// An undated folder with the same title corroborates words that can also
	// name a format: "Mr Holland's Opus/Mr Holland's Opus", "The UHD Journey".
	if folderYear == 0 && folderTitle != "" && onlyTitleWordFormats(surface) &&
		normalizeInferComparable(surface) == normalizeInferComparable(folderTitle) {
		return inferMovieStem{Title: surface, Remainder: bracketMetadata, Confidence: mediumIdentityConfidence}
	}
	// A number in the release suffix is not the movie's year. For example,
	// "Movie 480p 2001" names an undated movie with release metadata.
	titleSurface := surface
	remainder := bracketMetadata
	// A release year is a stronger title boundary than a word that can also
	// name a format ("Mr. Holland's Opus (1995)", "The.UHD.Journey.2014").
	// Resolution, source, and codec terms before the year still end the title.
	searchFrom := 0
	if year := inferYearBoundaryRe.FindStringIndex(surface); year != nil && year[0] > 0 &&
		!strings.Contains(surface[:year[0]], "[") && onlyTitleWordFormats(surface[:year[0]]) {
		searchFrom = year[1]
	}
	if location := movieTechnicalSuffixStart(surface[searchFrom:]); location >= 0 && searchFrom+location > 0 {
		location += searchFrom
		titleSurface = strings.TrimSpace(strings.TrimRight(surface[:location], " -_[({"))
		remainder = strings.TrimSpace(surface[location:] + " " + bracketMetadata)
	}
	if location := inferTerminalMultiRe.FindStringIndex(titleSurface); location != nil {
		remainder = strings.TrimSpace(titleSurface[location[0]:] + " " + remainder)
		titleSurface = strings.TrimSpace(titleSurface[:location[0]])
	}
	// "Show (2005) - S01E01" dates a show title; the explicit episode after the
	// year is not a movie release suffix.
	if match := inferBracketTitleYearRe.FindStringSubmatchIndex(titleSurface); match != nil && !hasExplicitEpisodeToken(surface[match[1]:]) {
		year, _ := strconv.Atoi(surface[match[4]:match[5]])
		title := strings.TrimRight(strings.TrimSpace(surface[match[2]:match[3]]), " -_")
		// Some renamers retain an existing bare year before adding a bracketed
		// one. Strip that duplicate only when the trusted folder corroborates
		// both the resulting title and the year, preserving numeric titles.
		if withoutYear, ok := strings.CutSuffix(title, " "+strconv.Itoa(year)); ok && folderYear == year &&
			strings.EqualFold(withoutYear, normalizeNameSeparators(folderTitle)) {
			title = withoutYear
		}
		return inferMovieStem{
			Title:      title,
			Year:       year,
			Remainder:  strings.TrimSpace(surface[match[1]:] + " " + bracketMetadata),
			Confidence: mediumIdentityConfidence,
		}
	}

	yearlessTitle := titleSurface
	if inferDiscTrackRe.MatchString(yearlessTitle) {
		return inferMovieStem{}
	}
	// Keep a complete title corroborated by its folder, including numeric
	// titles such as Blade Runner 2049. An explicitly dated folder still
	// supplies its year through the normal parsing below.
	if folderYear == 0 && folderTitle != "" && normalizeInferComparable(yearlessTitle) == normalizeInferComparable(folderTitle) {
		return inferMovieStem{Title: yearlessTitle, Remainder: remainder, Confidence: mediumIdentityConfidence}
	}

	folderTokens := normalizeInferTokens(folderTitle)
	tokens := strings.Fields(titleSurface)
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
		if idx+2 < len(tokens) {
			month, _ := strconv.Atoi(tokens[idx+1])
			day, _ := strconv.Atoi(tokens[idx+2])
			if month >= 1 && month <= 12 && day >= 1 && day <= 31 {
				continue
			}
		}
		titleTokens := tokens[:idx]
		if len(titleTokens) == 0 {
			continue
		}
		remainderTokens := strings.Fields(strings.Join(tokens[idx+1:], " ") + " " + remainder)
		if !inferHasSuffixEvidence(remainderTokens) && len(remainderTokens) > 0 {
			continue
		}

		titleCandidate := strings.TrimRight(collapseWhitespace(strings.Join(titleTokens, " ")), " -_")
		score := inferMovieStemScore(titleTokens, remainderTokens, folderTokens, folderYear, year)
		if score > bestScore || (score == bestScore && idx > bestIdx) {
			bestIdx = idx
			bestScore = score
			bestTitle = titleCandidate
			bestRemainder = collapseWhitespace(strings.Join(remainderTokens, " "))
		}
	}

	if bestIdx < 0 {
		if yearlessTitle != "" && remainder != "" {
			return inferMovieStem{Title: yearlessTitle, Remainder: remainder, Confidence: mediumIdentityConfidence}
		}
		return inferMovieStem{}
	}

	confidence := mediumIdentityConfidence
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

// Only bracket groups that identify release metadata are discarded. A bracket
// can be part of the title ([REC]), and parentheses can contain an alternate
// title (Run Lola Run (Lola rennt)). Neither is a reason to shorten a title.
func cleanMovieIdentitySurface(name string, folderTitle string) (string, string) {
	surface := stripInferProviderTags(name)
	var removed []string
	matches := inferMovieBracketRe.FindAllStringSubmatchIndex(surface, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		content := surface[match[2]:match[3]]
		before := strings.TrimSpace(surface[:match[0]])
		after := strings.TrimSpace(surface[match[1]:])
		metadata := inferMetadataBracketRe.MatchString(content)
		if before == "" && after != "" {
			metadata = metadata || inferTechnicalSuffixRe.MatchString(content) || inferReleasePrefixRe.MatchString(content) ||
				(folderTitle != "" && normalizeInferComparable(after) == normalizeInferComparable(folderTitle))
		}
		if metadata && (before != "" || after != "") {
			removed = append(removed, surface[match[0]:match[1]])
			surface = surface[:match[0]] + " " + surface[match[1]:]
		}
	}
	if len(removed) > 0 {
		surface = strings.Trim(surface, " ._-")
	}
	return normalizeNameSeparators(strings.TrimSpace(surface)), strings.Join(removed, " ")
}

// onlyTitleWordFormats reports whether every format term in a title is also an
// ordinary word ("Opus", "4K") rather than a release detail such as "1080p".
func onlyTitleWordFormats(title string) bool {
	for _, term := range inferTechnicalSuffixRe.FindAllString(title, -1) {
		term = strings.TrimFunc(term, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if !inferTitleWordFormatRe.MatchString(term) {
			return false
		}
	}
	return true
}

func movieTechnicalSuffixStart(surface string) int {
	location := -1
	if match := inferTechnicalSuffixRe.FindStringIndex(surface); match != nil {
		location = match[0]
	}
	for _, match := range inferMovieBracketRe.FindAllStringSubmatchIndex(surface, -1) {
		if inferTechnicalSuffixRe.MatchString(surface[match[2]:match[3]]) && (location < 0 || match[0] < location) {
			location = match[0]
		}
	}
	return location
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
	if inferTechnicalSuffixRe.MatchString(strings.Join(tokens, " ")) {
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
	surface, _ := cleanMovieIdentitySurface(name, "")
	if surface == "" {
		return "", 0, false
	}
	// A movie folder's explicit title/year boundary is stronger than a word
	// that can also name a codec or quality, such as "Opus" or "4K". The
	// filename parser still rejects years following technical suffixes.
	if match := inferBracketTitleYearRe.FindStringSubmatch(surface); match != nil {
		year, _ := strconv.Atoi(match[2])
		return strings.TrimSpace(match[1]), year, true
	}
	if ParseFolderIDs(name) != nil {
		return strings.TrimSpace(surface), 0, true
	}
	// A release folder can identify an obfuscated or generic disc filename.
	// Require a year and release suffix before trusting a parent directory.
	if stem := parseInferMovieStem(name, "", 0); stem.Year != 0 && stem.Remainder != "" {
		return stem.Title, stem.Year, true
	}
	return strings.TrimSpace(surface), 0, false
}

func normalizeNameSeparators(name string) string {
	// Keep punctuation in human titles ("Mr. Robot", "S.H.I.E.L.D.") while
	// reading ordinary dotted release words ("Mr.Robot") and underscores.
	surface := strings.ReplaceAll(name, "_", " ")
	acronyms := nameAcronymRe.FindAllStringIndex(surface, -1)
	if len(acronyms) == 0 {
		return collapseWhitespace(nameSeparatorRe.ReplaceAllString(surface, " $1"))
	}
	var builder strings.Builder
	builder.Grow(len(surface))
	previous, acronym := 0, 0
	for _, separator := range nameSeparatorRe.FindAllStringIndex(surface, -1) {
		builder.WriteString(surface[previous:separator[0]])
		for acronym < len(acronyms) && separator[0] >= acronyms[acronym][1] {
			acronym++
		}
		matched := surface[separator[0]:separator[1]]
		if acronym < len(acronyms) && separator[0] >= acronyms[acronym][0] {
			builder.WriteString(matched)
		} else {
			builder.WriteByte(' ')
			builder.WriteString(strings.TrimLeft(matched, "."))
		}
		previous = separator[1]
	}
	builder.WriteString(surface[previous:])
	return collapseWhitespace(builder.String())
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

package naming

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	labeledEpisodeRe         = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])s(?:eason)?[ ._-]*(\d{1,4})[ ._-]*(?:e(?:p(?:isode)?)?[ ._-]*|x[ ._-]*e?[ ._-]*)(\d+)`)
	xEpisodeRe               = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(\d{1,4})x(?:e)?(\d+)`)
	episodeOnlyRe            = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])e(?:p(?:isode)?)?[ ._-]*(\d+)`)
	leadingEpisodeRe         = regexp.MustCompile(`^\s*(\d+)(?:[ ._-]+|$)`)
	bracketEpisodeRe         = regexp.MustCompile(`\[(\d{1,5})\]`)
	trailingEpisodeRe        = regexp.MustCompile(`(?:[ ._-])(\d{1,5})(?:v\d+)?(?:$|[ ._\[(-])`)
	compactEpisodeRe         = regexp.MustCompile(`(?:^|[._])(\d{3})(?:$|[ ._-])`)
	dashEpisodeRe            = regexp.MustCompile(`^(\d)-(\d{2})(?:$|[ ._-])`)
	dayFirstDateRe           = regexp.MustCompile(`(?:^|[^0-9])\d{1,2}[-._ ]\d{1,2}[-._ ]\d{4}(?:$|[^0-9])`)
	unhandledEpisodePrefixRe = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:s?\d+x\d+|s(?:eason)?[ ._-]*\d+)[ ._-]*$`)
	episodeTechnicalRe       = regexp.MustCompile(`(?i)(?:^|[ ._[(\-])(?:\d{3,4}[pi]|web[ ._-]?dl|webrip|bluray|blu[ ._-]?ray|bdrip|dvdrip|hdtv|pdtv|x26[45]|h[ .]?26[45]|hevc|av1|aac|eac3|ac3|ddp|dts|truehd|flac|opus|nvenc)(?:$|[ ._\])\-]|[0-9])`)
	leadingGroupRe           = regexp.MustCompile(`^\[[^\]]+\]\s*`)
)

type episodeToken struct {
	season, episode int
	seasonKnown     bool
	compact         bool
	seriesTitle     string
	episodeEnd      int
	start, end      int
}

// parseEpisodeToken is shared by classification, episode linking, and title
// evidence. Unlabeled numbers need either a containing season directory or an
// explicitly declared series library. They never classify files in mixed or
// movie libraries by themselves.
func parseEpisodeToken(name string, directories []string, allowNumericSeason bool, allowUnseasoned ...bool) (episodeToken, bool) {
	for _, pattern := range []*regexp.Regexp{labeledEpisodeRe, xEpisodeRe} {
		for _, match := range pattern.FindAllStringSubmatchIndex(name, -1) {
			season, _ := strconv.Atoi(name[match[2]:match[3]])
			if (pattern == xEpisodeRe && !validXEpisodeSeason(season)) || !episodePartBoundary(name, match[5]) {
				continue
			}
			token := episodeToken{season: season, seasonKnown: true, episode: parseEpisodeNumber(name[match[4]:match[5]]), start: match[0], end: match[5]}
			return finishEpisodeToken(name, token), true
		}
	}

	season, hasSeason := 0, false
	if len(directories) > 0 {
		parent := strings.TrimSpace(directories[len(directories)-1])

		// A season's trailer/extra directories cannot supply missing episode
		// coordinates, even inside a declared series library.
		label := strings.NewReplacer(" ", "", ".", "", "_", "", "-", "").Replace(strings.ToLower(parent))
		_, exactExtra := extraSuffixKinds[label]
		_, pluralExtra := extraSuffixKinds[strings.TrimSuffix(label, "s")]
		if exactExtra || pluralExtra {
			return episodeToken{}, false
		}
		showDirectory := ""
		if len(directories) > 1 {
			showDirectory = directories[len(directories)-2]
		}
		season, hasSeason = seasonDirectoryNumber(parent, showDirectory, allowNumericSeason)
	}
	unseasoned := len(allowUnseasoned) > 0 && allowUnseasoned[0]
	if !hasSeason && !unseasoned {
		return episodeToken{}, false
	}
	if _, isDate := parseAirDate(name); isDate || dayFirstDateRe.MatchString(name) {
		return episodeToken{}, false
	}
	makeToken := func(start, end int, episode int) episodeToken {
		return finishEpisodeToken(name, episodeToken{season: season, seasonKnown: hasSeason, episode: episode, start: start, end: end})
	}
	for _, match := range episodeOnlyRe.FindAllStringSubmatchIndex(name, -1) {
		if compact := compactEpisodeMatch(name); compact != nil && compact[3] <= match[0] {
			prefix := strings.ToLower(strings.ReplaceAll(name[compact[3]:match[2]], " ", ""))
			if prefix == "-e" || prefix == "_e" {
				return parseCompactEpisode(name, compact, season, hasSeason), true
			}
		}
		if unhandledEpisodePrefixRe.MatchString(name[:match[0]]) {
			// A rejected strong coordinate cannot acquire a different season
			// through the weaker episode-only interpretation.
			return episodeToken{}, false
		}
		if episodePartBoundary(name, match[3]) {
			return makeToken(match[0], match[3], parseEpisodeNumber(name[match[2]:match[3]])), true
		}
	}
	// Ignore technical metadata when considering unlabeled numbers. Audio
	// layouts such as AAC5.1 must not replace the episode preceding them.
	if match := episodeTechnicalRe.FindStringIndex(name); match != nil {
		name = name[:match[0]]
	}
	if match := bracketEpisodeRe.FindStringSubmatchIndex(name); match != nil {
		return makeToken(match[0], match[1], parseEpisodeNumber(name[match[2]:match[3]])), true
	}
	if match := dashEpisodeRe.FindStringSubmatchIndex(name); match != nil {
		season, _ := strconv.Atoi(name[match[2]:match[3]])
		return finishEpisodeToken(name, episodeToken{season: season, seasonKnown: true, episode: parseEpisodeNumber(name[match[4]:match[5]]), start: match[0], end: match[5]}), true
	}
	if match := compactEpisodeMatch(name); match != nil {
		return parseCompactEpisode(name, match, season, hasSeason), true
	}
	if match := leadingEpisodeRe.FindStringSubmatchIndex(name); match != nil {
		digits := name[match[2]:match[3]]
		// A leading year is usually a title or a daily episode date. An
		// explicit E marker remains available for a four-digit episode.
		if len(digits) < 4 && episodeNumberBoundary(name, match[3]) {
			return makeToken(match[0], match[3], parseEpisodeNumber(digits)), true
		}
	}
	// A show title followed by a number is common in anime and disc rips.
	// Prefer the last candidate so numbers inside a series title are kept.
	matches := trailingEpisodeRe.FindAllStringSubmatchIndex(name, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		digits := name[match[2]:match[3]]
		number := parseEpisodeNumber(digits)
		if insideReleaseTag(name, match[2]) || number == 0 || (number >= 1928 && number <= 2500) || strings.Trim(name[:match[0]], " ._-") == "" {
			continue
		}
		if !episodeNumberBoundary(name, match[3]) {
			continue
		}
		return makeToken(match[0], match[3], number), true
	}
	return episodeToken{}, false
}

func insideReleaseTag(name string, index int) bool {
	prefix := name[:index]
	return strings.LastIndex(prefix, "[") > strings.LastIndex(prefix, "]") || strings.LastIndex(prefix, "(") > strings.LastIndex(prefix, ")")
}

func validXEpisodeSeason(season int) bool {
	// The x form can describe dimensions. Explicit S/E markers have no such
	// ambiguity and support long-running shows with season numbers above 199.
	// Year-numbered seasons start with the first year supported by metadata.
	return season < 200 || (season >= 1928 && season <= 2500)
}

func episodeNumberBoundary(name string, end int) bool {
	// Anime batches sometimes mark the last episode as E40END.
	if len(name)-end >= 3 && strings.EqualFold(name[end:end+3], "end") {
		if end+3 == len(name) {
			return true
		}
		next, _ := utf8.DecodeRuneInString(name[end+3:])
		if !unicode.IsLetter(next) && !unicode.IsDigit(next) {
			return true
		}
	}
	if episodeVersionEnd(name, end) != end {
		return true
	}
	if end == len(name) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[end:])
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
		return true
	}
	// Adjacent explicit markers are multi-episode forms (E01E02, 1x02x03).
	return (r == 'e' || r == 'E' || r == 'x' || r == 'X') && end+1 < len(name) && name[end+1] >= '0' && name[end+1] <= '9'
}

func episodePartBoundary(name string, end int) bool {
	if episodeNumberBoundary(name, end) {
		return true
	}
	if end >= len(name) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name[end:])
	// A single ASCII letter can label a split episode (E01a). Do not
	// accept a word attached to the number as episode evidence.
	if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
		if end+1 == len(name) {
			return true
		}
		next, _ := utf8.DecodeRuneInString(name[end+1:])
		if !unicode.IsLetter(next) && !unicode.IsDigit(next) {
			return true
		}
	}
	return false
}

func episodeVersionEnd(name string, end int) int {
	if end+1 >= len(name) || (name[end] != 'v' && name[end] != 'V') {
		return end
	}
	position := end + 1
	for position < len(name) && name[position] >= '0' && name[position] <= '9' {
		position++
	}
	if position == end+1 {
		return end
	}
	if position < len(name) {
		r, _ := utf8.DecodeRuneInString(name[position:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return end
		}
	}
	return position
}

func finishEpisodeToken(name string, token episodeToken) episodeToken {
	token.seriesTitle = cleanEpisodeSeriesTitle(name[:token.start])
	token.end = episodeVersionEnd(name, token.end)
	allowX := strings.Contains(strings.ToLower(name[token.start:token.end]), "x")
	for token.episode > 0 {
		end, number, ok := nextEpisodeInRange(name, token.end, token.season, allowX)
		if ok && token.compact && number >= 100 && number <= 999 && !strings.ContainsAny(name[token.end:end], "sSeExX") {
			if number/100 != token.season {
				break
			}
			number %= 100
		}
		if !ok || number <= token.episode || number <= token.episodeEnd {
			break
		}
		token.end = end
		token.episodeEnd = number
	}
	return token
}

func cleanEpisodeSeriesTitle(prefix string) string {
	prefix = strings.Trim(prefix, " ._-")
	// Release-group prefixes are not part of the series identity. Retain a
	// final bracketed name, as used by some anime release conventions.
	for match := leadingGroupRe.FindStringIndex(prefix); match != nil && match[1] < len(prefix); match = leadingGroupRe.FindStringIndex(prefix) {
		prefix = strings.TrimSpace(prefix[match[1]:])
	}
	prefix = strings.Trim(prefix, " []_.-")
	return normalizeNameSeparators(prefix)
}

// nextEpisodeInRange only reads a continuation immediately after the previous
// number. A number later in an episode title or a resolution is never a range.
func nextEpisodeInRange(name string, offset int, season int, allowX bool) (int, int, bool) {
	position := offset
	for position < len(name) && name[position] == ' ' {
		position++
	}
	spaced := position != offset
	separator := false
	separatorByte := byte(0)
	if position < len(name) && (name[position] == '-' || name[position] == '_') {
		separator = true
		separatorByte = name[position]
		position++
		beforeSpace := position
		for position < len(name) && name[position] == ' ' {
			position++
		}
		spaced = spaced || position != beforeSpace
	}
	if position >= len(name) {
		return 0, 0, false
	}
	marked := false
	marker := byte(0)
	if strings.ContainsRune("sSeExX", rune(name[position])) {
		marked = true
		marker = name[position]
		// An x264/x265 codec after an E-style coordinate is not another
		// episode. X continuations belong to the x-coordinate convention.
		if (marker == 'x' || marker == 'X') && !allowX {
			return 0, 0, false
		}
		position++
	}
	start := position
	for position < len(name) && name[position] >= '0' && name[position] <= '9' {
		position++
	}
	if position == start {
		return 0, 0, false
	}
	number := parseEpisodeNumber(name[start:position])
	// A separated x264/x265 token names a codec even after an x-style
	// coordinate. Adjacent continuations and repeated full coordinates keep
	// supporting real ranges such as 1x263x264 and 1x02-1x264.
	if (separator || spaced) && (marker == 'x' || marker == 'X') && (number == 264 || number == 265) {
		return 0, 0, false
	}
	// Repeated full coordinates must describe the same season.
	if (marker == 0 || marker == 's' || marker == 'S') && position < len(name) && strings.ContainsRune("xXeE", rune(name[position])) {
		if number != season {
			return 0, 0, false
		}
		marked = true
		position++
		start = position
		for position < len(name) && name[position] >= '0' && name[position] <= '9' {
			position++
		}
		number = parseEpisodeNumber(name[start:position])
	}
	if (!marked && (!separator || spaced || separatorByte == '_')) || number == 0 || !episodeNumberBoundary(name, position) {
		return 0, 0, false
	}
	return episodeVersionEnd(name, position), number, true
}

func parseEpisodeNumber(digits string) int {
	if len(digits) > maxEpisodeNumberDigits {
		return 0
	}
	number, _ := strconv.Atoi(digits)
	return number
}

// EpisodeTitleSuffix returns the text following a recognized episode token.
// Release-tag cleanup belongs to the caller that uses this title as evidence.
func EpisodeTitleSuffix(filePath string, libraryRoots ...string) string {
	base := filepath.Base(filePath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	libraryRoot := deepestContainingLibraryRoot(filePath, libraryRoots)
	directories := directorySegmentsWithinRoot(filePath, libraryRoot)
	allowNumericSeason := libraryRoot == "" || len(directories) > 1
	token, ok := parseEpisodeToken(stem, directories, allowNumericSeason, true)
	if !ok || token.episode == 0 {
		return ""
	}
	return strings.TrimLeft(stem[token.end:], " ._-")
}

func compactEpisodeMatch(name string) []int {
	match := compactEpisodeRe.FindStringSubmatchIndex(name)
	if match == nil || name[match[2]] == '0' || insideReleaseTag(name, match[2]) || strings.HasSuffix(strings.ToLower(name[:match[2]]), "h.") {
		return nil
	}
	if technical := episodeTechnicalRe.FindStringIndex(name); technical != nil && technical[0] < match[2] {
		return nil
	}
	return match
}

func parseCompactEpisode(name string, match []int, season int, hasSeason bool) episodeToken {
	compactSeason, _ := strconv.Atoi(name[match[2] : match[2]+1])
	if hasSeason && compactSeason != season {
		// A compact code is weaker than an explicit containing season.
		// Anime often uses three-digit absolute numbers inside season
		// folders; 301 in Season 21 must not become season 3 episode 1.
		return finishEpisodeToken(name, episodeToken{season: season, seasonKnown: true, episode: parseEpisodeNumber(name[match[2]:match[3]]), start: match[0], end: match[3]})
	}
	episode := parseEpisodeNumber(name[match[2]+1 : match[3]])
	return finishEpisodeToken(name, episodeToken{season: compactSeason, seasonKnown: true, compact: true, episode: episode, start: match[0], end: match[3]})
}

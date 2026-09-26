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
	digitRunRe               = regexp.MustCompile(`\d+`)
	seasonEpisodeDashRe      = regexp.MustCompile(`^\d-\d{2}(?:$|[ ._-])`)
	technicalXTokenRe        = regexp.MustCompile(`(?:(?:^|[^\p{L}\p{N}])(?:\d{3,4}x\d{2,4}|3x2|4x3|16x9|16x10|21x9)|\d\.\d+x(?:\d|26[45]))[ ._]*$`)
	aspectRatioRe            = regexp.MustCompile(`^(?:3x2|4x3|16x9|16x10|21x9)$`)
	titleYearAfterRe         = regexp.MustCompile(`[\(\[](?:19|20)\d{2}[\)\]]|^[ ._-]+(?:19|20)\d{2}(?:$|[ ._\-\[(])`)
	episodeFieldEndRe        = regexp.MustCompile(`^(?:-\d+)?(?:\s*$|\s*[\[(]|\s+-\s)`)
	leadingEpisodeSuffixRe   = regexp.MustCompile(`^\s*(?:$|[\[(]|-\s|-\d+(?:$|[\s\[(]))`)
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
			if (pattern == xEpisodeRe && (!validXEpisodeCoordinate(name, match) || inDatedEpisodeTitle(name, match[2]))) || !episodePartBoundary(name, match[5]) {
				continue
			}
			// A bare aspect ratio (Movie.Name.16x9) is a coordinate only in a
			// season folder or a series library.
			if pattern == xEpisodeRe && aspectRatioRe.MatchString(name[match[2]:match[5]]) && !hasSeriesContext(directories, allowNumericSeason, allowUnseasoned...) {
				continue
			}
			token := episodeToken{season: season, seasonKnown: true, episode: parseEpisodeNumber(name[match[4]:match[5]]), start: match[0], end: match[5]}
			return finishEpisodeToken(name, token), true
		}
	}

	season, hasSeason := 0, false
	showDirectory := ""
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
		if len(directories) > 1 {
			showDirectory = directories[len(directories)-2]
		}
		season, hasSeason = seasonDirectoryNumber(parent, showDirectory, allowNumericSeason)
		if !hasSeason {
			showDirectory = parent
		}
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
		if unhandledEpisodePrefixRe.MatchString(name[:match[0]]) && (name[match[0]] == '-' || !technicalXTokenRe.MatchString(name[:match[0]])) {
			// A rejected strong coordinate cannot acquire a different season
			// through the weaker episode-only interpretation. A resolution,
			// aspect ratio, or audio layout (1920x1080, 16x9, 2.0x2) was never
			// a coordinate, unless a dash attaches the marker as a range
			// (1920x1080-E15).
			return episodeToken{}, false
		}
		if episodePartBoundary(name, match[3]) {
			return makeToken(match[0], match[3], parseEpisodeNumber(name[match[2]:match[3]])), true
		}
	}
	// Ignore technical metadata when considering unlabeled numbers. Audio
	// layouts such as AAC5.1 must not replace the episode preceding them.
	if cut := technicalMetadataStart(name); cut >= 0 {
		name = name[:cut]
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
	if token, ok := delimitedEpisodeToken(name, showDirectory, makeToken); ok {
		return token, true
	}
	trailing := func() (episodeToken, bool) {
		// A show title followed by a number is common in anime and disc rips.
		// Prefer the last candidate so numbers inside a series title are kept.
		// Candidates may share a separator (The 100 05), so each search resumes
		// after the previous number rather than after its trailing separator.
		var matches [][]int
		for offset := 0; offset < len(name); {
			match := trailingEpisodeRe.FindStringSubmatchIndex(name[offset:])
			if match == nil {
				break
			}
			for j := range match {
				match[j] += offset
			}
			matches = append(matches, match)
			offset = match[3]
		}
		for i := len(matches) - 1; i >= 0; i-- {
			match := matches[i]
			digits := name[match[2]:match[3]]
			number := parseEpisodeNumber(digits)
			if insideReleaseTag(name, match[2]) || number == 0 || (number >= 1928 && number <= 2500) || strings.Trim(name[:match[0]], " ._-") == "" {
				continue
			}
			// A number that completes the show folder's title (The 100,
			// Room 104) names the show, not the episode.
			if completesShowTitle(name[:match[3]], showDirectory) {
				continue
			}
			if !episodeNumberBoundary(name, match[3]) {
				continue
			}
			return makeToken(match[0], match[3], number), true
		}
		return episodeToken{}, false
	}
	if match := leadingEpisodeRe.FindStringSubmatchIndex(name); match != nil {
		digits := name[match[2]:match[3]]
		// A leading year is usually a title or a daily episode date. An
		// explicit E marker remains available for a four-digit episode.
		if len(digits) < 4 && episodeNumberBoundary(name, match[3]) {
			// "90 Day Show 01" repeats the show title; its first number is
			// the episode only when no later number follows ("24 Title").
			if startsWithShowTitle(name, showDirectory) {
				if token, ok := trailing(); ok {
					return token, true
				}
			}
			return makeToken(match[0], match[3], parseEpisodeNumber(digits)), true
		}
	}
	return trailing()
}

// hasSeriesContext reports whether a file sits in a season folder or a
// declared series library.
func hasSeriesContext(directories []string, allowNumericSeason bool, allowUnseasoned ...bool) bool {
	if len(allowUnseasoned) > 0 && allowUnseasoned[0] {
		return true
	}
	if len(directories) == 0 {
		return false
	}
	showDirectory := ""
	if len(directories) > 1 {
		showDirectory = directories[len(directories)-2]
	}
	_, ok := seasonDirectoryNumber(directories[len(directories)-1], showDirectory, allowNumericSeason)
	return ok
}

// inDatedEpisodeTitle reports whether an NxM coordinate starting at index sits
// in the episode title of a dated name ("Show - 2016-10-25 - Title 2x4") rather
// than directly after the date ("Show - 2024-10-01 - 2024x246").
func inDatedEpisodeTitle(name string, index int) bool {
	date := airDateRe.FindStringSubmatchIndex(name)
	return date != nil && index > date[7] && strings.Trim(name[date[7]:index], " ._-") != ""
}

// delimitedEpisodeToken reads "Show - 05 - Part 2" and "05 - Title", where a
// spaced dash separates the episode number from the show and episode titles.
// The first such number wins: later numbers belong to the episode title. See
// yieldsToLaterEpisode for numbers that belong to the show instead.
func delimitedEpisodeToken(name, showDirectory string, makeToken func(start, end, episode int) episodeToken) (episodeToken, bool) {
	var candidates [][2]int
	for _, run := range digitRunRe.FindAllStringIndex(name, -1) {
		start, end := run[0], run[1]
		number := parseEpisodeNumber(name[start:end])
		after := episodeVersionEnd(name, end)
		if number == 0 || (number >= 1928 && number <= 2500) || insideReleaseTag(name, start) ||
			seasonEpisodeDashRe.MatchString(name[start:]) {
			continue
		}
		if start == 0 {
			// "9-1-1" and "90 Day Show" are titles, and a leading four-digit
			// number is usually a year.
			if end-start >= 4 || !leadingEpisodeSuffixRe.MatchString(name[after:]) {
				continue
			}
		} else {
			before := strings.TrimRight(name[:start], " ")
			if len(before) < 2 || before[len(before)-1] != '-' || !unicode.IsSpace(rune(before[len(before)-2])) {
				continue
			}
			if after < len(name) && !strings.ContainsRune(" \t-[(", rune(name[after])) {
				continue
			}
		}
		candidates = append(candidates, [2]int{start, end})
	}
	for i, candidate := range candidates {
		if i+1 < len(candidates) && yieldsToLaterEpisode(name, candidate, candidates[i+1], showDirectory) {
			continue
		}
		return makeToken(candidate[0], candidate[1], parseEpisodeNumber(name[candidate[0]:candidate[1]])), true
	}
	return episodeToken{}, false
}

// yieldsToLaterEpisode reports a number that belongs to the show rather than
// the episode: one completing the show folder's title ("24 - 05",
// "Mission - 3 - 05"), a leading number at a library root, or a season digit
// before a zero-padded episode ("Show - 1 - 05"). It yields only to a number
// that ends the field; "24 - 12-00 AM" is episode 24 with a time as its title.
func yieldsToLaterEpisode(name string, candidate, later [2]int, showDirectory string) bool {
	if !episodeFieldEndRe.MatchString(name[episodeVersionEnd(name, later[1]):]) {
		return false
	}
	return (candidate[0] == 0 && showDirectory == "") ||
		completesShowTitle(name[:candidate[1]], showDirectory) ||
		(candidate[1]-candidate[0] == 1 && name[later[0]] == '0')
}

func showFolderTitle(showDirectory string) string {
	title, _ := parseTitleYearCandidate(filepath.Base(showDirectory))
	return normalizeInferComparable(title)
}

func completesShowTitle(prefix, showDirectory string) bool {
	title := showFolderTitle(showDirectory)
	return title != "" && normalizeInferComparable(prefix) == title
}

func startsWithShowTitle(name, showDirectory string) bool {
	title := showFolderTitle(showDirectory)
	comparable := normalizeInferComparable(name)
	return title != "" && (comparable == title || strings.HasPrefix(comparable, title+" "))
}

// technicalMetadataStart finds where release details begin. A format word
// before any number can belong to the show title (Opus.COLORs - 02).
func technicalMetadataStart(name string) int {
	for _, match := range episodeTechnicalRe.FindAllStringIndex(name, -1) {
		term := strings.TrimFunc(name[match[0]:match[1]], func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if strings.ContainsAny(name[:match[0]], "0123456789") || !inferTitleWordFormatRe.MatchString(term) {
			return match[0]
		}
	}
	return -1
}

// hasExplicitEpisodeToken reports a labeled coordinate such as S01E01 or 1x01.
// Without series context, unlabeled numbers are not episode evidence.
func hasExplicitEpisodeToken(name string) bool {
	_, ok := parseEpisodeToken(name, nil, false)
	return ok
}

func insideReleaseTag(name string, index int) bool {
	prefix := name[:index]
	return strings.LastIndex(prefix, "[") > strings.LastIndex(prefix, "]") || strings.LastIndex(prefix, "(") > strings.LastIndex(prefix, ")")
}

func validXEpisodeCoordinate(name string, match []int) bool {
	// The x form can describe audio layouts (AAC 2.0x2, DD5.1x264) and
	// dimensions (2048x1080). Explicit S/E markers have no such ambiguity and
	// support long-running shows with season numbers above 199.
	start := match[2]
	episode := name[match[4]:match[5]]
	number, _ := strconv.Atoi(episode)
	// Release tags and aspect ratios before release details describe the
	// video: [16x9 1080p], Movie.4x3.DVDRip.
	if insideReleaseTag(name, start) {
		return false
	}
	if aspectRatioRe.MatchString(name[start:match[5]]) {
		// An aspect ratio yields to release details or an explicit episode
		// marker after it: Show.16x9.E02 is episode 2 of its season folder.
		if technical := episodeTechnicalRe.FindStringIndex(name[match[5]:]); technical != nil && technical[0] <= 1 {
			return false
		}
		if episodeOnlyRe.MatchString(name[match[5]:]) {
			return false
		}
	}
	// Audio layouts and aspect ratios put a decimal before a one-digit count
	// or codec: 2.0x2, 5.1x264, 2.35x1. Show names ending in a digit
	// (Babylon.5.1x01) do not.
	if start >= 2 && name[start-1] == '.' && isASCIIDigit(name[start-2]) && (start == 2 || !isASCIIDigit(name[start-3])) &&
		(len(episode) == 1 || number == 264 || number == 265) {
		return false
	}
	// A leading NxM followed by a release year is a title: 10x10 (2018),
	// 4x4.2019, 10x10 - 2018.
	if strings.Trim(name[:start], " ._-") == "" && titleYearAfterRe.MatchString(name[match[5]:]) {
		return false
	}
	season, _ := strconv.Atoi(name[start:match[3]])
	// A three-digit width with a multi-digit height describes a picture size
	// (128x96, 176x144, 720x480), but a year-numbered season keeps its day
	// count (2024x246); see below.
	if match[3]-start >= 3 && len(episode) >= 2 && !yearSeasonDay(season, number) {
		return false
	}
	if season < 200 {
		return true
	}
	return len(episode) <= 3 && yearSeasonDay(season, number)
}

// yearSeasonDay reports a year-numbered season coordinate. Such seasons start
// with the first year supported by metadata and hold at most one episode per
// day.
func yearSeasonDay(season, episode int) bool {
	return season >= 1928 && season <= 2500 && episode <= 366
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
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

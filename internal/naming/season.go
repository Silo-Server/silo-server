package naming

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// seasonDirectoryLabels name a season level. Accented labels also accept the
// decomposed spelling (a letter plus a combining accent) that macOS file
// systems and shares commonly store.
const seasonDirectoryLabels = bareSeasonDirectoryLabels + `|s|` + wordSeasonDirectoryLabels

// bareSeasonDirectoryLabels also name a season level with no number, as in a
// folder called just Season. The one-letter S and the word labels below are
// left out so category folders such as Series are not taken for seasons.
const bareSeasonDirectoryLabels = `season|staffel|stagione|sæson|temporada|kausi|s(?:ä|a\x{308})song|seizoen|seasong|sezon|sezona|sez(?:ó|o\x{301})na|sezonul|saison|시즌|シーズン|сезон`

// wordSeasonDirectoryLabels also begin ordinary titles (Series 7 Documentary).
// A folder directly in the library folder is usually the show, so there a bare
// one names a season only when release tags at most follow the number. Deeper
// folders, and names that start with the show title, keep the UK form
// Series 5 Matt Smith as season 5.
const wordSeasonDirectoryLabels = `series|s(?:e|é|e\x{301})ri[ae]`

var (
	seasonDirectoryTokenRe     = regexp.MustCompile(`(?i)(?:^|[ ._\-\[\]])(` + seasonDirectoryLabels + `)[ ._-]*(\d{1,4})(?:$|[ ._\-\[\]])`)
	reversedSeasonDirectoryRe  = regexp.MustCompile(`(?i)^(\d{1,4})(st|nd|rd|th)?[ ._-]+(` + seasonDirectoryLabels + `)(?:$|[ ._\-\[\]])`)
	wordSeasonDirectoryLabelRe = regexp.MustCompile(`(?i)^(?:` + wordSeasonDirectoryLabels + `)$`)
	seasonYearPrefixRe         = regexp.MustCompile(`^(?:19|20)\d{2}(?:$|[^0-9])`)
	bareSeasonDirectoryRe      = regexp.MustCompile(`(?i)^(?:` + bareSeasonDirectoryLabels + `)$`)
)

// isBareSeasonDirectory reports whether segment is a season label with no
// number, such as "Season". It is not a season number: the episode numbers in
// the file names still decide the season.
func isBareSeasonDirectory(segment string) bool {
	return bareSeasonDirectoryRe.MatchString(strings.TrimSpace(segment))
}

// isBareSeasonFolder is isBareSeasonDirectory for a folder that holds a file
// whose own series title is fileTitle. When that title is the folder name, as
// in Season/Season.S01E01.mkv, the folder is a show named "Season", not a
// season level.
func isBareSeasonFolder(segment, fileTitle string) bool {
	if !isBareSeasonDirectory(segment) {
		return false
	}
	return fileTitle == "" || normalizeComparableTitle(segment) != normalizeComparableTitle(fileTitle)
}

// seasonLabelStandsAlone reports whether a bare season label in a folder
// directly in the library folder names a season, given the text after its
// number (or, in the number-first form, after the label): only years,
// provider tags, and release tokens may follow.
func seasonLabelStandsAlone(label, rest string) bool {
	return !wordSeasonDirectoryLabelRe.MatchString(label) || onlyReleaseDetailsFollow(rest)
}

// onlyReleaseDetailsFollow reports whether rest, the text after a season
// number, holds nothing but years, provider tags, and release tokens.
func onlyReleaseDetailsFollow(rest string) bool {
	rest = stripFolderTags(rest)
	for {
		rest = strings.TrimLeft(rest, " ._-[](){}")
		if rest == "" {
			return true
		}
		if !seasonYearPrefixRe.MatchString(rest) {
			break
		}
		rest = rest[4:]
	}
	return startsWithMatch(episodeTechnicalRe, rest) || startsWithMatch(inferReleaseTokenRe, rest) || startsWithMatch(inferTechnicalSuffixRe, rest)
}

func startsWithMatch(re *regexp.Regexp, value string) bool {
	match := re.FindStringIndex(value)
	return match != nil && match[0] == 0
}

// mayBeShowFolder reports whether segment, a folder directly in a configured
// library folder, may be the show itself rather than a season folder. A file
// name that carries a different series title (Doctor Who S05E03 inside
// Series 5 Matt Smith) shows the folder is a season of a single-show library.
func mayBeShowFolder(segment, fileStem string) bool {
	if fileStem == "" {
		return true
	}
	token, ok := parseEpisodeToken(fileStem, nil, false, false)
	return !ok || token.seriesTitle == "" || normalizeComparableTitle(token.seriesTitle) == normalizeComparableTitle(segment)
}

// seasonDirectoryNumber recognizes a season directory without borrowing numbers
// from a show title. A title before the season label must match the parent show
// directory, as in Show/Show.S02.1080p, unless the parent carries provider IDs;
// Season 2 needs no parent evidence.
// topLevel marks a folder directly in the library folder, which may be the show.
func seasonDirectoryNumber(segment, parent string, allowNumeric, topLevel bool) (int, bool) {
	return seasonDirectoryNumberUnder(segment, parent, allowNumeric, topLevel, true)
}

// seasonDirectoryNumberUnder is seasonDirectoryNumber for a parent that may
// not be a show folder: a configured library folder's provider IDs name no
// show, so with parentMayBeShow false only a matching title counts.
func seasonDirectoryNumberUnder(segment, parent string, allowNumeric, topLevel, parentMayBeShow bool) (int, bool) {
	segment = strings.TrimSpace(segment)
	if specialsDirRe.MatchString(segment) {
		return 0, true
	}
	if allowNumeric && numericSeasonDirRe.MatchString(segment) {
		number, _ := strconv.Atoi(segment)
		return number, true
	}
	if match := reversedSeasonDirectoryRe.FindStringSubmatchIndex(segment); match != nil {
		label := segment[match[6]:match[7]]
		// 21st S.W.A.T. is a title; an ordinal needs a spelled-out label.
		ordinalLetter := match[4] >= 0 && strings.EqualFold(label, "s")
		if !ordinalLetter && (!topLevel || seasonLabelStandsAlone(label, segment[match[7]:])) {
			number, _ := strconv.Atoi(segment[match[2]:match[3]])
			return number, true
		}
	}
	parent = filepath.Base(strings.ReplaceAll(parent, `\`, "/"))
	parentName, _ := parseTitleYearCandidate(parent)
	parentTitle := normalizeInferComparable(parentName)
	// A provider-tagged parent names one show, so its season folders may
	// carry another title for it (an original-title show folder holding
	// international-title seasons: فوضى {tmdb-227604}/Fauda - S01). A folder
	// with its own provider tag names its own show. Only the short S01 form
	// counts there, with the number right after the S and only release
	// details after it: Season and Series also appear in titles (Open Season
	// 2, World Series 2020), as do a spaced S (U.S. 2020) and an S inside a
	// title (35mm S1 Scan), and a prefix with release details is a release
	// name whose group may look like one (Movie.2160p.WEB-DL-S3). A token
	// whose title matches the parent wins over this fallback.
	parentTagged := parentMayBeShow && ParseFolderIDs(parent) != nil && ParseFolderIDs(segment) == nil
	fallback, hasFallback := 0, false
	for _, match := range seasonDirectoryTokenRe.FindAllStringSubmatchIndex(segment, -1) {
		label := segment[match[2]:match[3]]
		number, _ := strconv.Atoi(segment[match[4]:match[5]])
		prefix := normalizeInferComparable(segment[:match[0]])
		if prefix != "" && (parentTitle == "" || prefix != parentTitle) {
			if parentTagged && !hasFallback && strings.EqualFold(label, "s") && match[3] == match[4] &&
				onlyReleaseDetailsFollow(segment[match[5]:]) && !inferTechnicalSuffixRe.MatchString(segment[:match[0]]) {
				fallback, hasFallback = number, true
			}
			continue
		}
		if prefix == "" && topLevel && !seasonLabelStandsAlone(label, segment[match[5]:]) {
			continue
		}
		return number, true
	}
	return fallback, hasFallback
}

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
const seasonDirectoryLabels = `season|staffel|stagione|sæson|temporada|kausi|s(?:ä|a\x{308})song|seizoen|seasong|sezon|sezona|sez(?:ó|o\x{301})na|sezonul|saison|시즌|シーズン|сезон|s|` + wordSeasonDirectoryLabels

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
	seasonYearSuffixRe         = regexp.MustCompile(`^[\[({]?(?:19|20)\d{2}(?:$|[^0-9])`)
)

// seasonLabelStandsAlone reports whether a bare season label in a folder
// directly in the library folder names a season, given the text after its
// number (or, in the number-first form, after the label).
func seasonLabelStandsAlone(label, rest string) bool {
	if !wordSeasonDirectoryLabelRe.MatchString(label) {
		return true
	}
	rest = strings.TrimLeft(stripFolderTags(rest), " ._-")
	if rest == "" || seasonYearSuffixRe.MatchString(rest) {
		return true
	}
	technical := episodeTechnicalRe.FindStringIndex(rest)
	return technical != nil && technical[0] == 0
}

// seasonDirectoryNumber recognizes a season directory without borrowing numbers
// from a show title. A title before the season label must match the parent show
// directory, as in Show/Show.S02.1080p; Season 2 needs no parent evidence.
// topLevel marks a folder directly in the library folder, which may be the show.
func seasonDirectoryNumber(segment, parent string, allowNumeric, topLevel bool) (int, bool) {
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
	for _, match := range seasonDirectoryTokenRe.FindAllStringSubmatchIndex(segment, -1) {
		prefix := normalizeInferComparable(segment[:match[0]])
		if prefix != "" && (parentTitle == "" || prefix != parentTitle) {
			continue
		}
		if prefix == "" && topLevel && !seasonLabelStandsAlone(segment[match[2]:match[3]], segment[match[5]:]) {
			continue
		}
		number, _ := strconv.Atoi(segment[match[4]:match[5]])
		return number, true
	}
	return 0, false
}

package naming

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const seasonDirectoryLabels = `season|staffel|stagione|sæson|temporada|series|kausi|säsong|seizoen|seasong|sezon|sezona|sezóna|sezonul|시즌|シーズン|сезон|s`

var (
	seasonDirectoryTokenRe    = regexp.MustCompile(`(?i)(?:^|[ ._\-\[\]])(?:` + seasonDirectoryLabels + `)[ ._-]*(\d{1,4})(?:$|[ ._\-\[\]])`)
	reversedSeasonDirectoryRe = regexp.MustCompile(`(?i)^(\d{1,4})[ ._-]+(?:` + seasonDirectoryLabels + `)(?:$|[ ._\-\[\]])`)
	// bareSeasonDirRe matches a folder named only with a season label and no
	// number. "series" and "s" are left out so category folders such as
	// "Series" are not taken for seasons.
	bareSeasonDirRe = regexp.MustCompile(`(?i)^(?:season|staffel|stagione|sæson|temporada|kausi|säsong|seizoen|seasong|sezon|sezona|sezóna|sezonul|시즌|シーズン|сезон)$`)
)

// isBareSeasonDirectory reports whether segment is a season label with no
// number, such as "Season". It is not a season number: the episode numbers in
// the file names still decide the season.
func isBareSeasonDirectory(segment string) bool {
	return bareSeasonDirRe.MatchString(strings.TrimSpace(segment))
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

// seasonDirectoryNumber recognizes a season directory without borrowing numbers
// from a show title. A title before the season label must match the parent show
// directory, as in Show/Show.S02.1080p; Season 2 needs no parent evidence.
func seasonDirectoryNumber(segment, parent string, allowNumeric bool) (int, bool) {
	segment = strings.TrimSpace(segment)
	if specialsDirRe.MatchString(segment) {
		return 0, true
	}
	if allowNumeric && numericSeasonDirRe.MatchString(segment) {
		number, _ := strconv.Atoi(segment)
		return number, true
	}
	if match := reversedSeasonDirectoryRe.FindStringSubmatch(segment); match != nil {
		number, _ := strconv.Atoi(match[1])
		return number, true
	}
	parent = filepath.Base(strings.ReplaceAll(parent, `\`, "/"))
	parentName, _ := parseTitleYearCandidate(parent)
	parentTitle := normalizeInferComparable(parentName)
	for _, match := range seasonDirectoryTokenRe.FindAllStringSubmatchIndex(segment, -1) {
		prefix := normalizeInferComparable(segment[:match[0]])
		if prefix != "" && (parentTitle == "" || prefix != parentTitle) {
			continue
		}
		number, _ := strconv.Atoi(segment[match[2]:match[3]])
		return number, true
	}
	return 0, false
}

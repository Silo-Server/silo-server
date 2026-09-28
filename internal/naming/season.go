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
)

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

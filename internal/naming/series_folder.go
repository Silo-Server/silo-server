package naming

import (
	"strconv"
	"strings"
)

var seriesTitleLigatures = strings.NewReplacer("æ", "ae", "œ", "oe")

// A filename and folder may spell the same ligature differently. This only
// corroborates the release year; it does not rewrite the title or group key.
func seriesTitlesShareYear(folder, filename string) bool {
	if inferTitlesCoherent(folder, filename) {
		return true
	}
	left := seriesTitleLigatures.Replace(normalizeInferComparable(folder))
	right := seriesTitleLigatures.Replace(normalizeInferComparable(filename))
	return left != "" && left == right
}

// A release pack may itself be the show root rather than a child season
// directory: The.Show.S01/E03.mkv still supplies a show title and season.
func parseSeriesFolderIdentity(name string) (title string, year, season int, seasonKnown bool) {
	// A dated show folder can contain coordinate-like title text (4x4,
	// S-245). Its explicit year establishes the complete title boundary.
	if title, year := parseTitleYearCandidate(name); year != 0 {
		surface := normalizeNameSeparators(stripFolderTags(name))
		if match := inferBracketTitleYearRe.FindStringIndex(surface); match != nil {
			suffix := strings.Trim(surface[match[1]:], " ._-")
			if token, ok := parseEpisodeToken(suffix, nil, false); ok {
				return title, year, token.season, token.seasonKnown
			}
			if season, ok := seasonDirectoryNumber(suffix, "", false); ok {
				return title, year, season, true
			}
		}
		return title, year, 0, false
	}
	if episode, ok := parseEpisodeToken(name, nil, false); ok && episode.seriesTitle != "" {
		title, year = parseTitleYearCandidate(episode.seriesTitle)
		return title, year, episode.season, episode.seasonKnown
	}
	if match := seasonDirectoryTokenRe.FindStringSubmatchIndex(name); match != nil {
		prefix := cleanEpisodeSeriesTitle(name[:match[0]])
		if prefix != "" && !episodeOnlyRe.MatchString(prefix) {
			title, year = parseTitleYearCandidate(prefix)
			season, _ = strconv.Atoi(name[match[2]:match[3]])
			return title, year, season, true
		}
	}
	title, year = parseTitleYearCandidate(strings.TrimSpace(name))
	return title, year, 0, false
}

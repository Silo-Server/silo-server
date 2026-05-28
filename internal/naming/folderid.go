package naming

import (
	"regexp"
	"strings"
	"unicode"
)

// folderIDPattern matches patterns like [tmdbid-27205], {tmdb-27205},
// [imdbid-tt1375666], {imdb-tt1375666}, [tvdbid-81189], {tvdb-81189}, etc.
// The regex captures the provider prefix and the ID value.
var folderIDPattern = regexp.MustCompile(`[{\[](tmdb|tmdbid|imdb|imdbid|tvdb|tvdbid)-([\w]+)[}\]]`)
var trailingImdbIDPattern = regexp.MustCompile(`(?i)(?:^|\s)(tt\d{7,8})$`)
var trailingNumericIDPattern = regexp.MustCompile(`(?:^|\s)(\d+)$`)

// bracketedBareImdbPattern matches a bare IMDb id wrapped in brackets without a
// provider prefix, e.g. [tt10011226] or {tt0095016} (Plex/Kodi-style tags). A
// tt-prefixed number is unambiguously IMDb.
var bracketedBareImdbPattern = regexp.MustCompile(`(?i)[{\[](tt\d{7,8})[}\]]`)

// ParseStructuredFolderIDs extracts only explicit provider IDs from a folder or
// file name, such as {tmdb-27205}, [imdbid-tt1375666], or [tt1375666]. It does
// not consider trailing bare IDs or folderType-based heuristics.
func ParseStructuredFolderIDs(name string) *FolderIDHints {
	matches := folderIDPattern.FindAllStringSubmatch(name, -1)
	hints := &FolderIDHints{}
	for _, m := range matches {
		provider := strings.ToLower(m[1])
		id := m[2]

		switch provider {
		case "tmdb", "tmdbid":
			hints.TmdbID = id
		case "imdb", "imdbid":
			hints.ImdbID = id
		case "tvdb", "tvdbid":
			hints.TvdbID = id
		}
	}

	if m := bracketedBareImdbPattern.FindStringSubmatch(name); m != nil && hints.ImdbID == "" {
		hints.ImdbID = strings.ToLower(m[1])
	}

	if hints.TmdbID == "" && hints.ImdbID == "" && hints.TvdbID == "" {
		return nil
	}
	return hints
}

// ParseFolderIDs extracts external provider IDs from a folder name.
// It supports bracket styles [] and {} with provider prefixes tmdb/tmdbid,
// imdb/imdbid, tvdb/tvdbid, plus bare trailing IDs. Bare numeric IDs are
// interpreted using folderType: series -> TVDB, everything else -> TMDB.
func ParseFolderIDs(folderName string, folderType string) *FolderIDHints {
	if hints := ParseStructuredFolderIDs(folderName); hints != nil {
		return hints
	}

	trimmed := strings.TrimSpace(folderName)
	if m := trailingImdbIDPattern.FindStringSubmatch(trimmed); m != nil {
		return &FolderIDHints{ImdbID: strings.ToLower(m[1])}
	}

	m := trailingNumericIDPattern.FindStringSubmatch(trimmed)
	if m == nil {
		return nil
	}

	id := m[1]
	if looksLikeYear(id) {
		return nil
	}

	// A bare trailing number is only an ID when appended to a real title. If the
	// name has no letters (e.g. "86", "22 7"), it's a numeric title, not an ID.
	// Trade-off: a folder named ONLY a bare provider id (e.g. "81189") with no
	// title text now returns nil rather than that id — acceptable because
	// Sonarr/Radarr never produce bare-number folders.
	if !containsLetter(trimmed) {
		return nil
	}

	if strings.EqualFold(strings.TrimSpace(folderType), "series") {
		return &FolderIDHints{TvdbID: id}
	}
	return &FolderIDHints{TmdbID: id}
}

func containsLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func looksLikeYear(value string) bool {
	return len(value) == 4 && value >= "1800" && value <= "2100"
}

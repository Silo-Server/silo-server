package naming

import (
	"regexp"
	"strings"
)

// folderIDPattern matches patterns like [tmdbid-27205], {tmdb-27205},
// [imdbid-tt1375666], {imdb-tt1375666}, [tvdbid-81189], {tvdb-81189}, etc.
// The regex captures the provider prefix and the ID value.
var folderIDPattern = regexp.MustCompile(`[{\[](tmdb|tmdbid|imdb|imdbid|tvdb|tvdbid)-([\w]+)[}\]]`)
var trailingImdbIDPattern = regexp.MustCompile(`(?i)(?:^|\s)(tt\d{7,8})$`)

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

// ParseFolderIDs extracts external provider IDs from a folder name. Mirroring
// Jellyfin's path-attribute model, only explicit evidence is honored: bracket
// tags with provider prefixes ({tmdb-27205}, [tvdbid-81189], [imdbid-tt1375666]),
// bracketed bare IMDb ids ([tt1375666]), and a trailing bare IMDb id — the
// "tt" prefix makes IMDb ids unambiguous without brackets. Bare trailing
// numbers are never treated as IDs: titles legitimately end in numbers
// ("District 9", "Beverly Hills 90210"), no mainstream tool emits bare-number
// tags, and a misparsed ID becomes a trusted match hint downstream where it
// silently produces a wrong match or blocks matching entirely.
func ParseFolderIDs(folderName string) *FolderIDHints {
	if hints := ParseStructuredFolderIDs(folderName); hints != nil {
		return hints
	}

	trimmed := strings.TrimSpace(folderName)
	if m := trailingImdbIDPattern.FindStringSubmatch(trimmed); m != nil {
		return &FolderIDHints{ImdbID: strings.ToLower(m[1])}
	}

	return nil
}

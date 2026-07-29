package naming

import (
	"regexp"
	"strings"

	"github.com/Silo-Server/silo-server/internal/providerid"
)

// folderIDPattern matches patterns like [tmdbid-27205], {tmdb-27205},
// [imdbid-tt1375666], {imdb-tt1375666}, [tvdbid-81189], {tvdb-81189}, etc.
// The regex captures the provider prefix and the ID value.
var folderIDPattern = regexp.MustCompile(`(?i)(?:\((tmdb|tmdbid|imdb|imdbid|tvdb|tvdbid)-([a-z0-9]+)\)|\[(tmdb|tmdbid|imdb|imdbid|tvdb|tvdbid)-([a-z0-9]+)\]|\{(tmdb|tmdbid|imdb|imdbid|tvdb|tvdbid)-([a-z0-9]+)\})`)
var trailingImdbIDPattern = regexp.MustCompile(`(?i)(?:^|\s)(tt\d{7,10})$`)

// bracketedBareImdbPattern matches a bare IMDb id wrapped in brackets without a
// provider prefix, e.g. [tt10011226] or {tt0095016} (Plex/Kodi-style tags). A
// tt-prefixed number is unambiguously IMDb.
var bracketedBareImdbPattern = regexp.MustCompile(`(?i)(?:\((tt\d{7,10})\)|\[(tt\d{7,10})\]|\{(tt\d{7,10})\})`)

// ParseStructuredFolderIDs extracts only explicit provider IDs from a folder or
// file name, such as {tmdb-27205}, [imdbid-tt1375666], or [tt1375666]. It does
// not consider trailing bare IDs or folderType-based heuristics.
func ParseStructuredFolderIDs(name string) *FolderIDHints {
	matches := folderIDPattern.FindAllStringSubmatch(name, -1)
	hints := &FolderIDHints{}
	for _, m := range matches {
		provider, id := "", ""
		for i := 1; i+1 < len(m); i += 2 {
			if m[i] != "" {
				provider, id = strings.ToLower(m[i]), strings.ToLower(m[i+1])
				break
			}
		}

		switch provider {
		case "tmdb", "tmdbid":
			if providerid.IsPositiveDecimal(id) {
				hints.TmdbID = id
			}
		case "imdb", "imdbid":
			if isIMDbProviderID(id) {
				hints.ImdbID = id
			}
		case "tvdb", "tvdbid":
			if providerid.IsPositiveDecimal(id) {
				hints.TvdbID = id
			}
		}
	}

	if m := bracketedBareImdbPattern.FindStringSubmatch(name); m != nil && hints.ImdbID == "" {
		for _, value := range m[1:] {
			if isIMDbProviderID(value) {
				hints.ImdbID = strings.ToLower(value)
				break
			}
		}
	}

	if hints.TmdbID == "" && hints.ImdbID == "" && hints.TvdbID == "" {
		return nil
	}
	return hints
}

func isIMDbProviderID(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if !trailingImdbIDPattern.MatchString(value) {
		return false
	}
	return providerid.IsPositiveDecimal(strings.TrimPrefix(value, "tt"))
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
	hints := ParseStructuredFolderIDs(folderName)

	// A trailing bare IMDb id complements structured tags for other providers
	// ("Show [tvdbid-81189] tt1375666"); an explicit structured imdb tag still
	// wins when both are present.
	trimmed := strings.TrimSpace(folderName)
	if m := trailingImdbIDPattern.FindStringSubmatch(trimmed); m != nil {
		if hints == nil {
			return &FolderIDHints{ImdbID: strings.ToLower(m[1])}
		}
		if hints.ImdbID == "" {
			hints.ImdbID = strings.ToLower(m[1])
		}
	}

	return hints
}

package historyimport

import (
	"fmt"
	"strings"
)

// Stored run warnings and unmatched reasons are diagnostics: they can carry
// upstream response bodies and internal errors, so run monitors never echo
// them. PublicWarning and PublicUnmatchedReason map the diagnostics this
// package writes to fixed summaries and anything else to generic text.

const (
	GenericRunWarning      = "An import item could not be processed."
	GenericUnmatchedReason = "No matching catalog item was imported."

	unmatchedNoProviderIDs     = "The source item has no TMDB, IMDb, or TVDB ID."
	unmatchedNotInLibrary      = "Nothing in the library has the same TMDB, IMDb, or TVDB ID."
	unmatchedAmbiguous         = "More than one library item has the same ID."
	unmatchedUnsupportedKind   = "This kind of item isn't imported."
	unmatchedNoEpisodeNumber   = "The source episode has no episode number."
	unmatchedEpisodeMissing    = "The show is in the library, but this episode isn't."
	unmatchedShowNoProviderIDs = "The source show has no TMDB, IMDb, or TVDB ID."
	unmatchedShowAmbiguous     = "More than one library show has the same ID."
	unmatchedShowMissing       = "The show isn't in the library."

	unmatchedWarningFormat = "unmatched items (%d): %s"

	warnEmbyFavoritesUnavailable = "fetching Emby favorites failed"
	warnEmbySeriesUnavailable    = "fetching Emby series metadata failed"
	warnEmbySeasonFavorites      = "skipped Emby season favorites (%d)"
	// Runs before the fixed text stored the upstream error after this prefix.
	legacyEmbyFavoritesPrefix = "fetching Emby favorites: "

	embyFavoritesUnavailableSummary = "Emby favorites couldn't be read, so none were imported."
	embySeriesUnavailableSummary    = "Emby show details couldn't be read, so some episodes may be unmatched."
)

// PublicWarning returns the monitor text for a stored run warning.
func PublicWarning(diagnostic string) string {
	var count int
	var reason string
	if n, _ := fmt.Sscanf(diagnostic, strings.TrimSuffix(unmatchedWarningFormat, "%s"), &count); n == 1 {
		_, reason, _ = strings.Cut(diagnostic, "): ")
		return fmt.Sprintf("Not matched (%d): %s", count, PublicUnmatchedReason(reason))
	}
	if n, _ := fmt.Sscanf(diagnostic, warnEmbySeasonFavorites, &count); n == 1 {
		return fmt.Sprintf("Season favorites skipped (%d): Silo can't favorite a season.", count)
	}
	switch {
	case diagnostic == warnEmbyFavoritesUnavailable, strings.HasPrefix(diagnostic, legacyEmbyFavoritesPrefix):
		return embyFavoritesUnavailableSummary
	case diagnostic == warnEmbySeriesUnavailable:
		return embySeriesUnavailableSummary
	}
	return GenericRunWarning
}

var unmatchedSummaries = map[string]bool{
	GenericUnmatchedReason: true, unmatchedNoProviderIDs: true, unmatchedNotInLibrary: true,
	unmatchedAmbiguous: true, unmatchedUnsupportedKind: true, unmatchedNoEpisodeNumber: true,
	unmatchedEpisodeMissing: true, unmatchedShowNoProviderIDs: true, unmatchedShowAmbiguous: true,
	unmatchedShowMissing: true,
}

// PublicUnmatchedReason returns the monitor text for a matcher reason. A
// summary maps to itself, so runs can count unmatched items by cause.
func PublicUnmatchedReason(diagnostic string) string {
	if unmatchedSummaries[diagnostic] {
		return diagnostic
	}
	// An episode reason lists every attempt; a series fallback is always the
	// last one and carries its own reason.
	if _, series, ok := strings.Cut(diagnostic, seriesMatchFailedPrefix); ok {
		switch {
		case series == missingProviderIDsReason:
			return unmatchedShowNoProviderIDs
		case strings.HasPrefix(series, ambiguousReasonPrefix):
			return unmatchedShowAmbiguous
		default:
			return unmatchedShowMissing
		}
	}
	decisive := diagnostic
	if i := strings.LastIndex(diagnostic, "; "); i >= 0 {
		decisive = diagnostic[i+2:]
	}
	switch {
	case decisive == missingProviderIDsReason:
		return unmatchedNoProviderIDs
	case decisive == missingEpisodeNumberReason:
		return unmatchedNoEpisodeNumber
	case decisive == unsupportedKindReason:
		return unmatchedUnsupportedKind
	case strings.HasPrefix(decisive, missingEpisodeReasonPrefix):
		return unmatchedEpisodeMissing
	case strings.HasPrefix(decisive, ambiguousReasonPrefix):
		return unmatchedAmbiguous
	case strings.HasPrefix(decisive, noMatchReasonPrefix) && strings.Contains(decisive, noMatchReasonMarker):
		return unmatchedNotInLibrary
	}
	return GenericUnmatchedReason
}

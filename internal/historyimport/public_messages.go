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

	// warnHiddenHistorySuppressed counts records the source gave no play time for
	// whose item this profile had removed from its history. The store keeps them
	// hidden, so every later run drops them the same way.
	warnHiddenHistorySuppressed = "skipped hidden history items (%d)"

	warnEmbyFavoritesUnavailable = "fetching Emby favorites failed"
	warnEmbySeriesUnavailable    = "fetching Emby series metadata failed"
	warnEmbySeasonFavorites      = "skipped Emby season favorites (%d)"
	// Runs before the fixed text stored the upstream error after this prefix.
	legacyEmbyFavoritesPrefix = "fetching Emby favorites: "

	warnJellyfinFavoritesUnavailable      = "fetching Jellyfin favorites failed"
	warnJellyfinFavoriteSeriesUnavailable = "fetching Jellyfin series metadata for favorites failed"

	embyFavoritesUnavailableSummary = "Emby favorites couldn't be read, so none were imported."
	embySeriesUnavailableSummary    = "Emby show details couldn't be read, so some episodes may be unmatched."

	jellyfinFavoritesUnavailableSummary      = "Jellyfin favorites couldn't be read, so some favorites may be missing."
	jellyfinFavoriteSeriesUnavailableSummary = "Jellyfin show details couldn't be read, so some favorite episodes may be unmatched."
)

// hiddenHistoryWarning is the stored diagnostic for records this profile keeps
// hidden. Runs record the count so the summary explains the gap between matched
// items and imported ones instead of leaving them in the plain skipped tally.
func hiddenHistoryWarning(count int) string {
	return fmt.Sprintf(warnHiddenHistorySuppressed, count)
}

// upsertHiddenHistoryWarning keeps that diagnostic current in warnings, returning
// the slice and the index it lives at (pass -1 the first time). A run updates it
// as it goes rather than adding it at the end, so a run that is canceled or fails
// partway still explains the records it skipped.
//
// It goes at the front, not the back. Only the first maxStoredWarnings entries
// are persisted, and a run noisy enough to fill that cap is exactly the one whose
// reader needs this count explained rather than trimmed away. Nothing else
// prepends, so index 0 survives the appends that follow.
func upsertHiddenHistoryWarning(warnings []string, index, count int) ([]string, int) {
	if index < 0 {
		return append([]string{hiddenHistoryWarning(count)}, warnings...), 0
	}
	warnings[index] = hiddenHistoryWarning(count)
	return warnings, index
}

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
	if n, _ := fmt.Sscanf(diagnostic, warnHiddenHistorySuppressed, &count); n == 1 {
		return fmt.Sprintf(
			"Not imported (%d): you removed these titles from this profile's history, and the source didn't say when they were played.",
			count)
	}
	switch {
	case diagnostic == warnEmbyFavoritesUnavailable, strings.HasPrefix(diagnostic, legacyEmbyFavoritesPrefix):
		return embyFavoritesUnavailableSummary
	case diagnostic == warnEmbySeriesUnavailable:
		return embySeriesUnavailableSummary
	case diagnostic == warnJellyfinFavoritesUnavailable:
		return jellyfinFavoritesUnavailableSummary
	case diagnostic == warnJellyfinFavoriteSeriesUnavailable:
		return jellyfinFavoriteSeriesUnavailableSummary
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

// GenericRunError is the monitor text for a stored failure that is not one of
// the messages written for users.
const GenericRunError = "The import failed. Review the source configuration before starting a new run."

// PublicRunError returns the monitor text for a stored run failure. Failures
// written for users pass through; anything else may carry an upstream
// response or an internal error and becomes GenericRunError.
func PublicRunError(message string) string {
	switch message {
	case "", ErrRunConfigurationChanged.Error(), LegacyDispatchUnavailableMessage, StaleRunInterruptedMessage,
		ErrPersonalCredentialsUnavailable.Error(), RunErrorSourceRejected, RunErrorStoppedEarly, RunErrorNotCompleted,
		PrivateAddressMessage, BlockedAddressMessage:
		return message
	default:
		return GenericRunError
	}
}

// PublicRun returns run with its stored diagnostics replaced by the text a
// run monitor may show the account that owns it. Every surface that sends a
// run to that account uses it; applying it twice is not supported.
func PublicRun(run Run) Run {
	run.ErrorMessage = PublicRunError(run.ErrorMessage)
	if run.Warnings != nil {
		warnings := make([]string, 0, len(run.Warnings))
		for _, warning := range run.Warnings {
			warnings = append(warnings, PublicWarning(warning))
		}
		run.Warnings = warnings
	}
	if run.UnmatchedSamples != nil {
		samples := make([]UnmatchedSample, len(run.UnmatchedSamples))
		copy(samples, run.UnmatchedSamples)
		for i := range samples {
			samples[i].Reason = PublicUnmatchedReason(samples[i].Reason)
		}
		run.UnmatchedSamples = samples
	}
	return run
}

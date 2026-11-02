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

	warnEmbyFavoritesUnavailable  = "fetching Emby favorites failed"
	warnEmbySeriesUnavailable     = "fetching Emby series metadata failed"
	warnEmbySeasonFavorites       = "skipped Emby season favorites (%d)"
	warnEmbyResumeListUnavailable = "fetching Emby continue watching failed"
	// warnContinueWatchingNotReconciled reports a failed pass dropping the
	// shows the source hid from its Continue Watching.
	warnContinueWatchingNotReconciled = "hiding shows hidden from the source's continue watching failed"
	// Runs before the fixed text stored the upstream error after this prefix.
	legacyEmbyFavoritesPrefix = "fetching Emby favorites: "

	warnJellyfinResumeUnavailable         = "fetching Jellyfin resume positions failed"
	warnJellyfinFavoritesUnavailable      = "fetching Jellyfin favorites failed"
	warnJellyfinFavoriteSeriesUnavailable = "fetching Jellyfin series metadata for favorites failed"
	// warnJellyfinLookbackUnsorted reports a lookback run that read the full
	// played history because the server did not return plays newest first, or
	// the list kept moving while the run read it.
	warnJellyfinLookbackUnsorted = "jellyfin played items were not newest first or kept moving; read the full played history"

	embyFavoritesUnavailableSummary      = "Emby favorites couldn't be read, so none were imported."
	embySeriesUnavailableSummary         = "Emby show details couldn't be read, so some episodes may be unmatched."
	embyResumeListUnavailableSummary     = "Emby's Continue Watching couldn't be read, so titles hidden there may appear in Continue Watching."
	continueWatchingNotReconciledSummary = "Shows hidden from the source's Continue Watching couldn't all be hidden here, so some may appear in Continue Watching."

	// Plex warnings keep the upstream error after their fixed text, so the
	// summaries below parse only the leading part.
	warnPlexSectionUnavailable         = "failed to fetch %s %s from section %q: %v" // watched|in-progress, noun, title, error
	warnPlexWatchlistUnavailablePrefix = "watchlist fetch failed: "
	warnPlexUnresolvedIDsMarker        = ": could not resolve external ids for "
	plexWatchlistIDScope               = "watchlist"
	plexAdminHistoryIDScope            = "plex admin history"
	warnPlexSeriesMetadataUnavailable  = "failed to fetch series metadata for %d of %d series"

	jellyfinResumeUnavailableSummary         = "Jellyfin in-progress items couldn't be read, so partly watched titles may be missing."
	jellyfinFavoritesUnavailableSummary      = "Jellyfin favorites couldn't be read, so some favorites may be missing."
	jellyfinFavoriteSeriesUnavailableSummary = "Jellyfin show details couldn't be read, so some favorite episodes may be unmatched."
	jellyfinLookbackUnsortedSummary          = "Jellyfin didn't list plays newest first, or its list kept changing during the read, so this run read the full played history instead of only the lookback window."
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
	if summary, ok := publicPlexWarning(diagnostic); ok {
		return summary
	}
	switch {
	case diagnostic == warnEmbyFavoritesUnavailable, strings.HasPrefix(diagnostic, legacyEmbyFavoritesPrefix):
		return embyFavoritesUnavailableSummary
	case diagnostic == warnEmbySeriesUnavailable:
		return embySeriesUnavailableSummary
	case diagnostic == warnEmbyResumeListUnavailable:
		return embyResumeListUnavailableSummary
	case diagnostic == warnContinueWatchingNotReconciled:
		return continueWatchingNotReconciledSummary
	case diagnostic == warnJellyfinResumeUnavailable:
		return jellyfinResumeUnavailableSummary
	case diagnostic == warnJellyfinFavoritesUnavailable:
		return jellyfinFavoritesUnavailableSummary
	case diagnostic == warnJellyfinFavoriteSeriesUnavailable:
		return jellyfinFavoriteSeriesUnavailableSummary
	case diagnostic == warnJellyfinLookbackUnsorted:
		return jellyfinLookbackUnsortedSummary
	}
	return GenericRunWarning
}

// publicPlexWarning summarizes the Plex diagnostics. Each names what the run
// left out, so a user knows a completed run is partial and worth re-running.
func publicPlexWarning(diagnostic string) (string, bool) {
	var state, noun, section string
	if n, _ := fmt.Sscanf(diagnostic, strings.TrimSuffix(warnPlexSectionUnavailable, ": %v"), &state, &noun, &section); n == 3 &&
		(noun == "movies" || noun == "episodes") {
		switch state {
		case "watched":
			return fmt.Sprintf("Plex section %q couldn't be read, so its watched %s weren't imported.", section, noun), true
		case "in-progress":
			return fmt.Sprintf("Plex section %q couldn't be read, so its partly watched %s weren't imported.", section, noun), true
		}
	}
	if strings.HasPrefix(diagnostic, warnPlexWatchlistUnavailablePrefix) {
		return "Your Plex watchlist couldn't be read, so it wasn't imported.", true
	}
	var missing, total int
	// The second count covers only the items that needed a lookup, so the
	// summary leaves it out.
	if scope, counts, ok := strings.Cut(diagnostic, warnPlexUnresolvedIDsMarker); ok {
		if n, _ := fmt.Sscanf(counts, "%d of %d", &missing, &total); n == 2 {
			switch {
			case scope == plexWatchlistIDScope && missing == 1:
				return "Plex couldn't identify 1 watchlist title, so it wasn't added to the watchlist.", true
			case scope == plexWatchlistIDScope:
				return fmt.Sprintf("Plex couldn't identify %d watchlist titles, so they weren't added to the watchlist.", missing), true
			case scope == plexAdminHistoryIDScope && missing == 1:
				return "Plex couldn't identify 1 played title, so it stays unmatched.", true
			case scope == plexAdminHistoryIDScope:
				return fmt.Sprintf("Plex couldn't identify %d played titles, so they stay unmatched.", missing), true
			}
		}
	}
	if n, _ := fmt.Sscanf(diagnostic, warnPlexSeriesMetadataUnavailable, &missing, &total); n == 2 {
		return fmt.Sprintf("Plex show details couldn't be read for %d of %d shows, so some of their episodes may be unmatched.", missing, total), true
	}
	return "", false
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
		ErrPersonalCredentialsUnavailable.Error(), RunErrorSourceRejected, RunErrorStoppedEarly, RunErrorNotCompleted, RunErrorSourceTooLarge,
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

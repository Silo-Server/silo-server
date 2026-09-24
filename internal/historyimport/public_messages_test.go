package historyimport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
)

// The summaries are keyed on matcher output, so these cases run the real
// matcher rather than restating its strings.
func TestPublicUnmatchedReasonSummarizesMatcherReasons(t *testing.T) {
	t.Parallel()

	two := []mediaLookupRow{{ContentID: "a"}, {ContentID: "b"}}
	repo := &matcherRepoStub{
		mediaByExternal: map[string][]mediaLookupRow{
			"movie:tmdb_id:1":    two,
			"series:tvdb_id:700": {{ContentID: "series-700"}},
			"series:tvdb_id:800": two,
		},
	}
	matcher := NewMatcher(repo)
	cases := []struct {
		name   string
		record Record
		want   string
	}{
		{"no provider IDs", Record{Kind: KindMovie, Title: "Home Movie"}, "The source item has no TMDB, IMDb, or TVDB ID."},
		{"not in library", Record{Kind: KindMovie, TMDBID: "335984", IMDbID: "tt1856101"}, "Nothing in the library has the same TMDB, IMDb, or TVDB ID."},
		{"ambiguous movie", Record{Kind: KindMovie, TMDBID: "1"}, "More than one library item has the same ID."},
		{"unsupported kind", Record{Kind: "trailer"}, "This kind of item isn't imported."},
		{"episode without number", Record{Kind: KindEpisode, TVDBID: "5"}, "The source episode has no episode number."},
		{"episode missing from matched show", Record{Kind: KindEpisode, SeasonNumber: 1, EpisodeNumber: 9, SeriesTVDBID: "700"}, "The show is in the library, but this episode isn't."},
		{"show not in library", Record{Kind: KindEpisode, SeasonNumber: 1, EpisodeNumber: 1, SeriesTVDBID: "900", SeriesTMDBID: "901"}, "The show isn't in the library."},
		{"show without IDs", Record{Kind: KindEpisode, SeasonNumber: 1, EpisodeNumber: 1, SeriesTitle: "Mystery"}, "The source show has no TMDB, IMDb, or TVDB ID."},
		{"ambiguous show", Record{Kind: KindEpisode, SeasonNumber: 1, EpisodeNumber: 1, SeriesTVDBID: "800"}, "More than one library show has the same ID."},
	}
	for _, tc := range cases {
		match, reason, err := matcher.Match(context.Background(), tc.record)
		if err != nil || match != nil {
			t.Fatalf("%s: Match = %v, %v; want an unmatched reason", tc.name, match, err)
		}
		if got := PublicUnmatchedReason(reason); got != tc.want {
			t.Errorf("%s: PublicUnmatchedReason(%q) = %q, want %q", tc.name, reason, got, tc.want)
		}
	}
	if got := PublicUnmatchedReason("pq: connection reset"); got != GenericUnmatchedReason {
		t.Errorf("unknown reason = %q, want generic text", got)
	}
}

func TestPublicWarningSummarizesKnownDiagnostics(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		fmt.Sprintf(unmatchedWarningFormat, 3, missingProviderIDsReason):      "Not matched (3): The source item has no TMDB, IMDb, or TVDB ID.",
		fmt.Sprintf(warnEmbySeasonFavorites, 2):                               "Season favorites skipped (2): Silo can't favorite a season.",
		warnEmbyFavoritesUnavailable:                                          embyFavoritesUnavailableSummary,
		legacyEmbyFavoritesPrefix + "emby http 500: <html>stack trace</html>": embyFavoritesUnavailableSummary,
		warnEmbySeriesUnavailable:                                             embySeriesUnavailableSummary,
		warnJellyfinFavoritesUnavailable:                                      jellyfinFavoritesUnavailableSummary,
		warnJellyfinFavoriteSeriesUnavailable:                                 jellyfinFavoriteSeriesUnavailableSummary,
		"favorites import: add movie-1: pq: deadlock detected":                GenericRunWarning,
	}
	for diagnostic, want := range cases {
		if got := PublicWarning(diagnostic); got != want {
			t.Errorf("PublicWarning(%q) = %q, want %q", diagnostic, got, want)
		}
	}
}

func TestPublicUnmatchedReasonKeepsSummaries(t *testing.T) {
	t.Parallel()

	for summary := range unmatchedSummaries {
		if got := PublicUnmatchedReason(summary); got != summary {
			t.Errorf("PublicUnmatchedReason(%q) = %q, want the summary unchanged", summary, got)
		}
	}
	stored := fmt.Sprintf(unmatchedWarningFormat, 4, unmatchedNotInLibrary)
	if got := PublicWarning(stored); got != "Not matched (4): "+unmatchedNotInLibrary {
		t.Errorf("PublicWarning(%q) = %q", stored, got)
	}
}

func TestTagUnreachableMarksOnlyNetworkFailures(t *testing.T) {
	t.Parallel()

	dial := &url.Error{Op: "Get", URL: "http://emby.example.test", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	if err := tagUnreachable(dial); !errors.Is(err, ErrSourceUnreachable) || !errors.Is(err, dial) {
		t.Fatalf("dial failure = %v, want tagged and wrapped", err)
	}
	rejected := UpstreamHTTPError(http.StatusUnauthorized)
	if err := tagUnreachable(rejected); errors.Is(err, ErrSourceUnreachable) {
		t.Fatalf("HTTP 401 was tagged unreachable: %v", err)
	}
}

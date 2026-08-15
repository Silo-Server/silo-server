package historyimport

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type matcherRepoStub struct {
	episodeByExternal map[string][]mediaLookupRow
	mediaByExternal   map[string][]mediaLookupRow
	mediaByTitleYear  map[string][]mediaLookupRow
	episodeBySeries   *Match
	episodesBySeries  []Match

	episodeBySeriesCalls   int
	episodeBySeriesID      string
	episodeBySeriesSeason  int
	episodeBySeriesEpisode int
	episodesBySeriesID     string
	episodesBySeriesSeason *int
	episodesBySeriesAt     *time.Time
	mediaByExternalCalls   int
}

func (s *matcherRepoStub) MatchMediaByExternalID(_ context.Context, kind, column, value string) ([]mediaLookupRow, error) {
	s.mediaByExternalCalls++
	return s.mediaByExternal[fmt.Sprintf("%s:%s:%s", kind, column, value)], nil
}

func (s *matcherRepoStub) MatchMediaByTitleYear(_ context.Context, kind, title string, year int) ([]mediaLookupRow, error) {
	return s.mediaByTitleYear[fmt.Sprintf("%s:%s:%d", kind, title, year)], nil
}

func (s *matcherRepoStub) MatchEpisodeByExternalID(_ context.Context, column, value string) ([]mediaLookupRow, error) {
	return s.episodeByExternal[column+":"+value], nil
}

func (s *matcherRepoStub) MatchEpisodeBySeriesExternalID(_ context.Context, column, value string, seasonNumber, episodeNumber int) ([]mediaLookupRow, error) {
	return s.episodeByExternal[fmt.Sprintf("series:%s:%s:%d:%d", column, value, seasonNumber, episodeNumber)], nil
}

func (s *matcherRepoStub) MatchEpisodeBySeries(_ context.Context, seriesID string, seasonNumber, episodeNumber int) (*Match, error) {
	s.episodeBySeriesCalls++
	s.episodeBySeriesID = seriesID
	s.episodeBySeriesSeason = seasonNumber
	s.episodeBySeriesEpisode = episodeNumber
	return s.episodeBySeries, nil
}

func (s *matcherRepoStub) MatchEpisodesBySeries(_ context.Context, seriesID string, seasonNumber *int, watchedAt *time.Time) ([]Match, error) {
	s.episodesBySeriesID = seriesID
	if seasonNumber != nil {
		season := *seasonNumber
		s.episodesBySeriesSeason = &season
	}
	if watchedAt != nil {
		at := *watchedAt
		s.episodesBySeriesAt = &at
	}
	return s.episodesBySeries, nil
}

func TestMatcherMatchLeavesExpandsWholeShowToEpisodes(t *testing.T) {
	t.Parallel()

	watchedAt := time.Date(2025, time.October, 15, 20, 0, 0, 0, time.UTC)
	repo := &matcherRepoStub{
		mediaByExternal: map[string][]mediaLookupRow{
			"series:tmdb_id:1396": {{ContentID: "series-1", Title: "Breaking Bad", Year: 2008}},
		},
		episodesBySeries: []Match{
			{MediaItemID: "episode-1", Kind: KindEpisode},
			{MediaItemID: "episode-2", Kind: KindEpisode},
		},
	}

	matches, reason, err := NewMatcher(repo).MatchLeaves(context.Background(), Record{
		Kind: KindSeries, Title: "Breaking Bad", Year: 2008, TMDBID: "1396", LastPlayedAt: &watchedAt,
	})
	if err != nil || reason != "" {
		t.Fatalf("MatchLeaves error = %v, reason = %q", err, reason)
	}
	if len(matches) != 2 || matches[0].MediaItemID != "episode-1" || matches[1].MediaItemID != "episode-2" {
		t.Fatalf("matches = %#v", matches)
	}
	if repo.episodesBySeriesID != "series-1" || repo.episodesBySeriesSeason != nil ||
		repo.episodesBySeriesAt == nil || !repo.episodesBySeriesAt.Equal(watchedAt) {
		t.Fatalf("unexpected expansion scope: id=%q season=%v watched_at=%v",
			repo.episodesBySeriesID, repo.episodesBySeriesSeason, repo.episodesBySeriesAt)
	}
}

func TestMatcherMatchLeavesPreservesSpecialsSeasonZero(t *testing.T) {
	t.Parallel()

	repo := &matcherRepoStub{
		mediaByExternal: map[string][]mediaLookupRow{
			"series:tvdb_id:75978": {{ContentID: "series-1", Title: "Family Guy", Year: 1999}},
		},
		episodesBySeries: []Match{{MediaItemID: "special-1", Kind: KindEpisode}},
	}
	matches, reason, err := NewMatcher(repo).MatchLeaves(context.Background(), Record{
		Kind: KindSeason, SeriesTitle: "Family Guy", SeriesYear: 1999,
		SeriesTVDBID: "75978", SeasonNumber: 0,
	})
	if err != nil || reason != "" || len(matches) != 1 {
		t.Fatalf("matches = %#v, reason = %q, err = %v", matches, reason, err)
	}
	if repo.episodesBySeriesSeason == nil || *repo.episodesBySeriesSeason != 0 {
		t.Fatalf("season scope = %v, want explicit season 0", repo.episodesBySeriesSeason)
	}
}

func TestMatcherMatchEpisode_UsesExternalIDsWithoutSeasonEpisodeNumbers(t *testing.T) {
	t.Parallel()

	repo := &matcherRepoStub{
		episodeByExternal: map[string][]mediaLookupRow{
			"tvdb_id:10602140": {{ContentID: "episode-1", Title: "Echo: The Invisible Hand", Year: 2025}},
		},
	}
	matcher := NewMatcher(repo)

	match, reason, err := matcher.Match(context.Background(), Record{
		Kind:   KindEpisode,
		Title:  "Echo: The Invisible Hand",
		Year:   2025,
		TVDBID: "10602140",
	})
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty string", reason)
	}
	if match == nil {
		t.Fatal("expected match, got nil")
	}
	if match.MediaItemID != "episode-1" {
		t.Fatalf("MediaItemID = %q, want %q", match.MediaItemID, "episode-1")
	}
	if repo.episodeBySeriesCalls != 0 {
		t.Fatalf("episodeBySeriesCalls = %d, want 0", repo.episodeBySeriesCalls)
	}
	if repo.mediaByExternalCalls != 0 {
		t.Fatalf("mediaByExternalCalls = %d, want 0", repo.mediaByExternalCalls)
	}
	if match.Kind != KindEpisode {
		t.Fatalf("Kind = %q, want %q", match.Kind, KindEpisode)
	}
}

func TestMatcherMatchEpisode_AllowsSeasonZeroSpecials(t *testing.T) {
	t.Parallel()

	repo := &matcherRepoStub{
		mediaByExternal: map[string][]mediaLookupRow{
			"series:tvdb_id:75978": {{ContentID: "series-1", Title: "Family Guy", Year: 1999}},
		},
		episodeBySeries: &Match{
			MediaItemID: "episode-special-1",
			Kind:        KindEpisode,
			Title:       "Special",
			Year:        1999,
		},
	}
	matcher := NewMatcher(repo)

	match, reason, err := matcher.Match(context.Background(), Record{
		Kind:          KindEpisode,
		SeriesTitle:   "Family Guy",
		SeriesYear:    1999,
		SeriesTVDBID:  "75978",
		SeasonNumber:  0,
		EpisodeNumber: 1,
	})
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty string", reason)
	}
	if match == nil || match.MediaItemID != "episode-special-1" {
		t.Fatalf("match = %+v, want episode-special-1", match)
	}
	if repo.episodeBySeriesCalls != 1 {
		t.Fatalf("episodeBySeriesCalls = %d, want 1", repo.episodeBySeriesCalls)
	}
	if repo.episodeBySeriesID != "series-1" || repo.episodeBySeriesSeason != 0 || repo.episodeBySeriesEpisode != 1 {
		t.Fatalf("MatchEpisodeBySeries called with (%q, %d, %d), want (series-1, 0, 1)",
			repo.episodeBySeriesID, repo.episodeBySeriesSeason, repo.episodeBySeriesEpisode)
	}
}

func TestMatcherMatchEpisode_UsesCoordinatesBeforeAmbiguousSeriesRows(t *testing.T) {
	t.Parallel()

	repo := &matcherRepoStub{
		mediaByExternal: map[string][]mediaLookupRow{
			"series:tvdb_id:296188": {
				{ContentID: "angie-library-a", Title: "Angie Tribeca", Year: 2016},
				{ContentID: "angie-library-b", Title: "Angie Tribeca", Year: 2016},
			},
		},
		episodeByExternal: map[string][]mediaLookupRow{
			"series:tvdb_id:296188:1:8": {{ContentID: "angie-s01e08", Title: "Murder in the First Class", Year: 2016}},
		},
	}
	matcher := NewMatcher(repo)

	match, reason, err := matcher.Match(context.Background(), Record{
		Kind:          KindEpisode,
		SeriesTitle:   "Angie Tribeca",
		SeriesYear:    2016,
		SeriesTVDBID:  "296188",
		SeasonNumber:  1,
		EpisodeNumber: 8,
	})
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty string", reason)
	}
	if match == nil || match.MediaItemID != "angie-s01e08" {
		t.Fatalf("match = %+v, want angie-s01e08", match)
	}
}

func TestMatcherMatchSeries_PrefersTMDBForEmbyFavorite(t *testing.T) {
	t.Parallel()

	repo := &matcherRepoStub{
		mediaByExternal: map[string][]mediaLookupRow{
			"series:tmdb_id:94997":  {{ContentID: "house-of-the-dragon", Title: "House of the Dragon", Year: 2022}},
			"series:tvdb_id:425793": {{ContentID: "making-of", Title: "The House That Dragons Built", Year: 2022}},
		},
	}
	matcher := NewMatcher(repo)

	match, reason, err := matcher.Match(context.Background(), Record{
		Kind:       KindSeries,
		Title:      "House of the Dragon",
		Year:       2022,
		TMDBID:     "94997",
		TVDBID:     "425793",
		PreferTMDB: true,
	})
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty string", reason)
	}
	if match == nil || match.MediaItemID != "house-of-the-dragon" {
		t.Fatalf("match = %+v, want TMDB-backed House of the Dragon", match)
	}
}

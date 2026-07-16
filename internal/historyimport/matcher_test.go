package historyimport

import (
	"context"
	"fmt"
	"testing"
)

type matcherRepoStub struct {
	episodeByExternal map[string][]mediaLookupRow
	mediaByExternal   map[string][]mediaLookupRow
	mediaByTitleYear  map[string][]mediaLookupRow
	episodeBySeries   *Match

	episodeBySeriesCalls   int
	episodeBySeriesID      string
	episodeBySeriesSeason  int
	episodeBySeriesEpisode int
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

func (s *matcherRepoStub) MatchEpisodeBySeries(_ context.Context, seriesID string, seasonNumber, episodeNumber int) (*Match, error) {
	s.episodeBySeriesCalls++
	s.episodeBySeriesID = seriesID
	s.episodeBySeriesSeason = seasonNumber
	s.episodeBySeriesEpisode = episodeNumber
	return s.episodeBySeries, nil
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

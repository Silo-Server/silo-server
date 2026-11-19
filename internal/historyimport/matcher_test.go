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

func (s *matcherRepoStub) MatchEpisodesBySeries(_ context.Context, seriesID string, seasonNumber *int, watchedAt *time.Time, _ []int) ([]Match, error) {
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

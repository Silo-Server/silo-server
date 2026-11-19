package metadata

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type targetRefreshProvider struct {
	slug             string
	searchResults    []SearchResult
	seasons          []SeasonResult
	episodesBySeason map[int][]EpisodeResult
	episodesErr      error
	episodeRequests  []int
	lastEpisodesReq  EpisodesRequest
}

func (p *targetRefreshProvider) Slug() string {
	if p.slug != "" {
		return p.slug
	}
	return "target-refresh"
}

func (p *targetRefreshProvider) Name() string { return "target-refresh" }

func (p *targetRefreshProvider) ForTypes() []string { return []string{"series"} }

func (p *targetRefreshProvider) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	return append([]SearchResult(nil), p.searchResults...), nil
}

func (p *targetRefreshProvider) GetSeasons(context.Context, SeasonsRequest) ([]SeasonResult, error) {
	return append([]SeasonResult(nil), p.seasons...), nil
}

func (p *targetRefreshProvider) GetEpisodes(_ context.Context, req EpisodesRequest) ([]EpisodeResult, error) {
	p.episodeRequests = append(p.episodeRequests, req.SeasonNumber)
	p.lastEpisodesReq = req
	if p.episodesErr != nil {
		return nil, p.episodesErr
	}
	return append([]EpisodeResult(nil), p.episodesBySeason[req.SeasonNumber]...), nil
}

func seedTargetRefreshHarness(provider *targetRefreshProvider) (*testHarness, string, string, string) {
	h := newTestHarness()
	h.service.seasonRepo = newFakeSeasonRepo()
	h.service.episodeRepo = newFakeEpisodeRepo()

	expiresAt := time.Now().Add(time.Hour)
	h.service.chainCache = map[string]chainCacheEntry{
		"0:series":  {providers: []Provider{provider}, expiresAt: expiresAt},
		"0:season":  {providers: []Provider{provider}, expiresAt: expiresAt},
		"0:episode": {providers: []Provider{provider}, expiresAt: expiresAt},
	}

	seriesID := "series-target-refresh"
	seasonID := "season-s05"
	episodeID := "episode-s05e03"
	h.itemRepo.items[seriesID] = &models.MediaItem{
		ContentID:               seriesID,
		Type:                    "series",
		Title:                   "Target Refresh Show",
		TmdbID:                  "12345",
		Status:                  "matched",
		DefaultMetadataLanguage: "en",
	}
	_ = h.service.seasonRepo.Upsert(context.Background(), &models.Season{
		ContentID:      seasonID,
		SeriesID:       seriesID,
		SeasonNumber:   5,
		MetadataSource: "provider",
	})
	_ = h.service.episodeRepo.Upsert(context.Background(), &models.Episode{
		ContentID:      episodeID,
		SeriesID:       seriesID,
		SeasonID:       seasonID,
		SeasonNumber:   5,
		EpisodeNumber:  3,
		MetadataSource: "provider",
	})
	_ = h.service.episodeRepo.Upsert(context.Background(), &models.Episode{
		ContentID:      "episode-s05e04",
		SeriesID:       seriesID,
		SeasonID:       seasonID,
		SeasonNumber:   5,
		EpisodeNumber:  4,
		Title:          "Old Episode 4",
		MetadataSource: "provider",
	})

	return h, seriesID, seasonID, episodeID
}

func TestRefreshScheduledTargetEpisodePersistsOnlySelectedEpisode(t *testing.T) {
	provider := &targetRefreshProvider{
		seasons: []SeasonResult{{
			SeasonNumber: 5,
			Title:        "Provider Season 5",
		}},
		episodesBySeason: map[int][]EpisodeResult{
			5: {
				{SeasonNumber: 5, EpisodeNumber: 3, Title: "Provider Episode 3", Overview: "Updated only this one"},
				{SeasonNumber: 5, EpisodeNumber: 4, Title: "Provider Episode 4", Overview: "Should not be persisted"},
			},
		},
	}
	h, seriesID, _, episodeID := seedTargetRefreshHarness(provider)
	if err := h.service.episodeRepo.Upsert(context.Background(), &models.Episode{
		ContentID:      episodeID,
		SeriesID:       seriesID,
		SeasonID:       "season-s05",
		SeasonNumber:   5,
		EpisodeNumber:  3,
		Title:          "Episode 3",
		MetadataSource: "scanner_fallback",
	}); err != nil {
		t.Fatalf("seed placeholder episode: %v", err)
	}

	if err := h.service.RefreshScheduledTarget(context.Background(), RefreshTargetEpisode, episodeID); err != nil {
		t.Fatalf("RefreshScheduledTarget episode: %v", err)
	}

	if !reflect.DeepEqual(provider.episodeRequests, []int{5}) {
		t.Fatalf("episode provider requests = %v, want [5]", provider.episodeRequests)
	}
	updated, err := h.service.episodeRepo.GetBySeriesAndNumber(context.Background(), seriesID, 5, 3)
	if err != nil {
		t.Fatalf("GetBySeriesAndNumber S05E03: %v", err)
	}
	if updated.Title != "Provider Episode 3" || updated.Overview != "Updated only this one" {
		t.Fatalf("S05E03 = (%q, %q), want provider metadata", updated.Title, updated.Overview)
	}
	unchanged, err := h.service.episodeRepo.GetBySeriesAndNumber(context.Background(), seriesID, 5, 4)
	if err != nil {
		t.Fatalf("GetBySeriesAndNumber S05E04: %v", err)
	}
	if unchanged.Title != "Old Episode 4" || unchanged.Overview != "" {
		t.Fatalf("S05E04 changed during episode refresh: (%q, %q)", unchanged.Title, unchanged.Overview)
	}
}

package metadata

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
)

type targetRefreshProvider struct {
	slug             string
	searchResults    []SearchResult
	seasons          []SeasonResult
	episodesBySeason map[int][]EpisodeResult
	episodeRequests  []int
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

func TestResolveSeriesRefreshProviderIDsUsesStaleSuppressedSelectionIdentity(t *testing.T) {
	const (
		staleTMDBID       = "12345"
		replacementTMDBID = "67890"
	)
	provider := &targetRefreshProvider{
		slug: contentid.ProviderTMDB,
		searchResults: []SearchResult{{
			Name: "Target Refresh Show", Year: 2020, Provider: contentid.ProviderTMDB,
			ProviderIDs: map[string]string{contentid.ProviderTMDB: replacementTMDBID},
		}},
	}
	h, seriesID, _, _ := seedTargetRefreshHarness(provider)
	series := h.itemRepo.items[seriesID]
	series.Year = 2020
	series.TmdbID = staleTMDBID
	staleRepo := newFakeStaleIDRepo()
	staleRepo.set(seriesID, &models.StaleMediaID{
		ContentID: seriesID, Provider: contentid.ProviderTMDB, ProviderID: staleTMDBID,
	})
	h.service.staleIDRepo = staleRepo

	providerIDs, err := h.service.resolveSeriesRefreshProviderIDs(context.Background(), series, 0, "en")
	if err != nil {
		t.Fatalf("resolveSeriesRefreshProviderIDs: %v", err)
	}
	if providerIDs[contentid.ProviderTMDB] != replacementTMDBID {
		t.Fatalf("resolved tmdb id = %q, want %s", providerIDs[contentid.ProviderTMDB], replacementTMDBID)
	}
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

func TestRefreshScheduledTargetSeasonPersistsSeasonScope(t *testing.T) {
	provider := &targetRefreshProvider{
		seasons: []SeasonResult{{
			SeasonNumber: 5,
			Title:        "Provider Season 5",
			Overview:     "Season overview",
		}},
		episodesBySeason: map[int][]EpisodeResult{
			5: {
				{SeasonNumber: 5, EpisodeNumber: 3, Title: "Provider Episode 3", Overview: "Episode 3 overview"},
				{SeasonNumber: 5, EpisodeNumber: 4, Title: "Provider Episode 4", Overview: "Episode 4 overview"},
			},
		},
	}
	h, seriesID, seasonID, _ := seedTargetRefreshHarness(provider)

	if err := h.service.RefreshScheduledTarget(context.Background(), RefreshTargetSeason, seasonID); err != nil {
		t.Fatalf("RefreshScheduledTarget season: %v", err)
	}

	season, err := h.service.seasonRepo.GetByID(context.Background(), seasonID)
	if err != nil {
		t.Fatalf("Get season: %v", err)
	}
	if season.Title != "Provider Season 5" || season.Overview != "Season overview" {
		t.Fatalf("season = (%q, %q), want provider metadata", season.Title, season.Overview)
	}
	for episodeNumber, wantOverview := range map[int]string{3: "Episode 3 overview", 4: "Episode 4 overview"} {
		episode, err := h.service.episodeRepo.GetBySeriesAndNumber(context.Background(), seriesID, 5, episodeNumber)
		if err != nil {
			t.Fatalf("GetBySeriesAndNumber S05E%02d: %v", episodeNumber, err)
		}
		if episode.Overview != wantOverview {
			t.Fatalf("S05E%02d overview = %q, want %q", episodeNumber, episode.Overview, wantOverview)
		}
	}
}

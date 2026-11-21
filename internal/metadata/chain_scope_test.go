package metadata

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// numberingProvider is a series provider that numbers a show its own way and
// counts the calls it gets, so a test can tell which chain a refresh used.
type numberingProvider struct {
	slug     string
	title    string
	episodes map[int][]int // season -> episode numbers

	mu    sync.Mutex
	calls int
}

func (p *numberingProvider) Slug() string       { return p.slug }
func (p *numberingProvider) Name() string       { return p.slug }
func (p *numberingProvider) ForTypes() []string { return []string{"series"} }

func (p *numberingProvider) called() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *numberingProvider) count() {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
}

func (p *numberingProvider) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	p.count()
	return nil, nil
}

func (p *numberingProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	p.count()
	return &MetadataResult{
		HasMetadata: true,
		ProviderIDs: map[string]string{p.slug: req.ProviderIDs[p.slug]},
		Title:       p.title,
		Overview:    p.title + " overview",
	}, nil
}

func (p *numberingProvider) GetSeasons(context.Context, SeasonsRequest) ([]SeasonResult, error) {
	p.count()
	var seasons []SeasonResult
	for season := range p.episodes {
		seasons = append(seasons, SeasonResult{SeasonNumber: season, Title: fmt.Sprintf("%s season %d", p.slug, season)})
	}
	slices.SortFunc(seasons, func(a, b SeasonResult) int { return a.SeasonNumber - b.SeasonNumber })
	return seasons, nil
}

func (p *numberingProvider) GetEpisodes(_ context.Context, req EpisodesRequest) ([]EpisodeResult, error) {
	p.count()
	var episodes []EpisodeResult
	for _, number := range p.episodes[req.SeasonNumber] {
		episodes = append(episodes, EpisodeResult{
			SeasonNumber:  req.SeasonNumber,
			EpisodeNumber: number,
			Title:         fmt.Sprintf("%s S%02dE%02d", p.slug, req.SeasonNumber, number),
		})
	}
	return episodes, nil
}

const chainScopeSeriesID = "series-chain-scope"

// seedChainScopeHarness seeds a matched series the plugin numbers as S01E01-02
// and S02E01, while the all-providers fallback (folder 0) has a provider that
// numbers the same show as one season of four episodes. libraries lists the
// libraries holding the series; each gets the plugin-only chain.
func seedChainScopeHarness(t *testing.T, libraries ...int) (*testHarness, *numberingProvider, *numberingProvider) {
	t.Helper()
	plugin := &numberingProvider{slug: "plugin-meta", title: "Plugin Show", episodes: map[int][]int{1: {1, 2}, 2: {1}}}
	other := &numberingProvider{slug: "tmdb", title: "Other Show", episodes: map[int][]int{1: {1, 2, 3, 4}}}

	h := newTestHarness()
	h.service.seasonRepo = newFakeSeasonRepo()
	h.service.episodeRepo = newFakeEpisodeRepo()
	expiresAt := time.Now().Add(time.Hour)
	h.service.chainCache = make(map[string]chainCacheEntry)
	for _, level := range []string{"series", "season", "episode"} {
		h.service.chainCache[chainCacheKeyForTest(0, level)] = chainCacheEntry{providers: []Provider{plugin, other}, expiresAt: expiresAt}
		for _, folderID := range libraries {
			h.service.chainCache[chainCacheKeyForTest(folderID, level)] = chainCacheEntry{providers: []Provider{plugin}, expiresAt: expiresAt}
		}
	}

	ctx := context.Background()
	h.itemRepo.items[chainScopeSeriesID] = &models.MediaItem{
		ContentID:               chainScopeSeriesID,
		Type:                    "series",
		Title:                   "Plugin Show",
		TmdbID:                  "100",
		Status:                  "matched",
		DefaultMetadataLanguage: "en",
	}
	for _, folderID := range libraries {
		if err := h.libraryRepo.Upsert(ctx, chainScopeSeriesID, folderID, time.Now()); err != nil {
			t.Fatalf("link library %d: %v", folderID, err)
		}
	}
	for _, number := range []int{1, 2} {
		if err := h.service.seasonRepo.Upsert(ctx, &models.Season{
			ContentID: fmt.Sprintf("season-%d", number), SeriesID: chainScopeSeriesID, SeasonNumber: number, MetadataSource: "provider",
		}); err != nil {
			t.Fatalf("seed season %d: %v", number, err)
		}
	}
	for _, e := range []struct{ season, episode int }{{1, 1}, {1, 2}, {2, 1}} {
		if err := h.service.episodeRepo.Upsert(ctx, &models.Episode{
			ContentID:      fmt.Sprintf("episode-s%02de%02d", e.season, e.episode),
			SeriesID:       chainScopeSeriesID,
			SeasonID:       fmt.Sprintf("season-%d", e.season),
			SeasonNumber:   e.season,
			EpisodeNumber:  e.episode,
			MetadataSource: "provider",
		}); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
	}
	return h, plugin, other
}

func episodeNumbers(t *testing.T, h *testHarness) []string {
	t.Helper()
	repo := h.service.episodeRepo.(*fakeEpisodeRepo)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	var numbers []string
	for _, episode := range repo.episodes {
		if episode.SeriesID == chainScopeSeriesID {
			numbers = append(numbers, fmt.Sprintf("S%02dE%02d", episode.SeasonNumber, episode.EpisodeNumber))
		}
	}
	slices.Sort(numbers)
	return numbers
}

// A refresh that names no library (scheduled, on-demand stale, trailer
// recovery) must use the chain of the library holding the item, not the
// all-providers fallback. Issue #4605.
func TestScheduledRefreshUsesTheItemLibraryChain(t *testing.T) {
	for _, target := range []struct {
		targetType string
		contentID  string
	}{
		{RefreshTargetItem, chainScopeSeriesID},
		{RefreshTargetSeason, "season-1"},
		{RefreshTargetEpisode, "episode-s01e02"},
	} {
		t.Run(target.targetType, func(t *testing.T) {
			h, plugin, other := seedChainScopeHarness(t, 7)

			if err := h.service.RefreshScheduledTarget(context.Background(), target.targetType, target.contentID); err != nil {
				t.Fatalf("RefreshScheduledTarget: %v", err)
			}

			if plugin.called() == 0 {
				t.Fatal("the library's plugin was never asked")
			}
			if calls := other.called(); calls != 0 {
				t.Fatalf("a provider outside the library's chain was asked %d times", calls)
			}
			if got, want := episodeNumbers(t, h), []string{"S01E01", "S01E02", "S02E01"}; !slices.Equal(got, want) {
				t.Fatalf("episodes = %v, want only the plugin's numbering %v", got, want)
			}
		})
	}
}

// An item in no library has no chain of its own, so it keeps the
// all-providers fallback.
func TestScheduledRefreshOfAnItemInNoLibraryKeepsTheFallback(t *testing.T) {
	h, _, other := seedChainScopeHarness(t)

	if err := h.service.RefreshScheduledTarget(context.Background(), RefreshTargetItem, chainScopeSeriesID); err != nil {
		t.Fatalf("RefreshScheduledTarget: %v", err)
	}
	if other.called() == 0 {
		t.Fatal("the fallback chain was not used for an item in no library")
	}
}

// An item in several libraries merges their chains in library ID order, so a
// provider only one library names still runs and a provider named twice runs
// once, at its first position. The local tier stays first even when only a
// later library enables it. A disabled library is left out while an enabled
// one holds the item; an item only disabled libraries hold keeps them all.
func TestResolveLibrariesChainMergesInLibraryOrder(t *testing.T) {
	nfo := &numberingProvider{slug: "nfo"}
	tmdb := &PluginProvider{installationID: 5, capabilityID: "tmdb"}
	tvdb := &PluginProvider{installationID: 6, capabilityID: "tvdb"}
	plugin := &PluginProvider{installationID: 7, capabilityID: "plugin-meta"}
	for _, tc := range []struct {
		name    string
		enabled map[int]bool
		want    []string
	}{
		{name: "both enabled", enabled: map[int]bool{3: true, 9: true}, want: []string{"nfo", "tmdb", "tvdb", "plugin-meta"}},
		{name: "one disabled", enabled: map[int]bool{3: false, 9: true}, want: []string{"nfo", "tvdb", "plugin-meta"}},
		{name: "all disabled", enabled: map[int]bool{3: false, 9: false}, want: []string{"nfo", "tmdb", "tvdb", "plugin-meta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			expiresAt := time.Now().Add(time.Hour)
			h.service.chainCache = map[string]chainCacheEntry{
				// Library 3 switched local metadata off.
				chainCacheKeyForTest(3, "series"): {providers: []Provider{tmdb, tvdb}, expiresAt: expiresAt},
				chainCacheKeyForTest(9, "series"): {providers: []Provider{nfo, tvdb, plugin}, expiresAt: expiresAt},
			}
			folders := &fakeMetadataFolderRepo{folders: map[int]*models.MediaFolder{}}
			for folderID, enabled := range tc.enabled {
				folders.folders[folderID] = &models.MediaFolder{ID: folderID, Enabled: enabled}
				if err := h.libraryRepo.Upsert(context.Background(), "item", folderID, time.Now()); err != nil {
					t.Fatalf("link library: %v", err)
				}
			}
			h.service.folderRepo = folders

			ctx, err := h.service.withItemChainScope(context.Background(), "item", 0)
			if err != nil {
				t.Fatalf("withItemChainScope: %v", err)
			}
			chain, err := h.service.resolveChainCached(ctx, 0, "series")
			if err != nil {
				t.Fatalf("resolveChainCached: %v", err)
			}
			var slugs []string
			for _, provider := range chain {
				slugs = append(slugs, provider.Slug())
			}
			if !slices.Equal(slugs, tc.want) {
				t.Fatalf("merged chain = %v, want %v", slugs, tc.want)
			}
		})
	}
}

package jellycompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type countingCompatImageResolver struct {
	singleCalls int
	batchCalls  int
	variants    []string
	paths       []string
}

func (r *countingCompatImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	r.singleCalls++
	return "single:" + variant + ":" + path
}

func (r *countingCompatImageResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	resolved := r.ResolveImageURLsWithExpiry(context.Background(), paths, variant)
	urls := make(map[string]string, len(resolved))
	for path, value := range resolved {
		urls[path] = value.URL
	}
	return urls
}

func (r *countingCompatImageResolver) ResolveImageURLWithExpiry(_ context.Context, path string, variant string) catalog.ResolvedImageURL {
	r.singleCalls++
	return catalog.ResolvedImageURL{URL: "single:" + variant + ":" + path}
}

func (r *countingCompatImageResolver) ResolveImageURLsWithExpiry(_ context.Context, paths []string, variant string) map[string]catalog.ResolvedImageURL {
	r.batchCalls++
	r.variants = append(r.variants, variant)
	r.paths = append(r.paths, paths...)
	resolved := make(map[string]catalog.ResolvedImageURL, len(paths))
	for _, path := range paths {
		resolved[path] = catalog.ResolvedImageURL{URL: "batch:" + variant + ":" + path}
	}
	return resolved
}

// progressCountingStoreProvider is a test double that records calls to
// ForUser and ListProgressByMediaItems. Used to assert that BrowseItems
// does not duplicate the handler-level user-data fetch.
type progressCountingStoreProvider struct {
	store *progressCountingStore
}

func newProgressCountingStoreProvider() *progressCountingStoreProvider {
	return &progressCountingStoreProvider{store: &progressCountingStore{}}
}

func (p *progressCountingStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	p.store.forUserCalls++
	return p.store, nil
}

func (p *progressCountingStoreProvider) Close() error { return nil }

// progressCountingStore is a userstore.UserStore stub that counts calls to
// ListProgressByMediaItems. The nil embedded store catches unexpected methods.
type progressCountingStore struct {
	userstore.UserStore
	forUserCalls           int
	listProgressCalls      int
	lastListedMediaItemIDs []string
}

func (s *progressCountingStore) ListProgressByMediaItems(_ context.Context, _ string, mediaItemIDs []string) (map[string]userstore.WatchProgress, error) {
	s.listProgressCalls++
	s.lastListedMediaItemIDs = mediaItemIDs
	return map[string]userstore.WatchProgress{}, nil
}

func (s *progressCountingStore) ListCompletedHistoryItems(context.Context, userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	return nil, nil
}
func (s *progressCountingStore) RemoveWatchedFromWatchlist(context.Context, string) (bool, error) {
	return true, nil
}

// stubBrowseSource is a deterministic browseSource for testing
// directContentService without a Postgres pool.
type stubBrowseSource struct {
	items            []*models.MediaItem
	total            int
	calls            []stubBrowseCall
	crossLibraryCall []stubCrossLibraryCall
}

type stubBrowseCall struct {
	filters      catalog.BrowseFilters
	includeTotal bool
}

type stubCrossLibraryCall struct {
	base       catalog.BrowseFilters
	libraryIDs []int
}

func (s *stubBrowseSource) BrowsePage(_ context.Context, filters catalog.BrowseFilters, includeTotal bool) (*catalog.BrowseResult, error) {
	s.calls = append(s.calls, stubBrowseCall{filters: filters, includeTotal: includeTotal})

	start := min(max(filters.Offset, 0), len(s.items))
	end := min(start+max(filters.Limit, 0), len(s.items))
	total := 0
	if includeTotal {
		total = s.total
	}
	return &catalog.BrowseResult{
		Items:   append([]*models.MediaItem(nil), s.items[start:end]...),
		Total:   total,
		HasMore: end < len(s.items),
	}, nil
}

func (s *stubBrowseSource) BrowseRecentlyAddedAcrossLibraries(_ context.Context, base catalog.BrowseFilters, libraryIDs []int) (*catalog.BrowseResult, error) {
	s.crossLibraryCall = append(s.crossLibraryCall, stubCrossLibraryCall{base: base, libraryIDs: libraryIDs})
	end := min(max(base.Limit, 0), len(s.items))
	return &catalog.BrowseResult{
		Items:   append([]*models.MediaItem(nil), s.items[:end]...),
		Total:   s.total,
		HasMore: end < len(s.items),
	}, nil
}

func (s *stubBrowseSource) ListGenres(_ context.Context, _ catalog.BrowseFilters) ([]string, error) {
	return nil, nil
}

type recordingCatalogSearchProvider struct {
	requests []catalog.CatalogSearchRequest
	result   *catalog.CatalogSearchResult
}

func (p *recordingCatalogSearchProvider) Search(_ context.Context, req catalog.CatalogSearchRequest) (*catalog.CatalogSearchResult, error) {
	p.requests = append(p.requests, req)
	if p.result != nil {
		return p.result, nil
	}
	return &catalog.CatalogSearchResult{}, nil
}

// newDirectContentServiceForTest builds a directContentService with stubbed
// catalog dependencies. Useful for behavioral tests that don't need real
// Postgres state.
func newDirectContentServiceForTest(browse browseSource, provider userstore.UserStoreProvider) *directContentService {
	return &directContentService{
		browseRepo:    browse,
		storeProvider: provider,
	}
}

func TestSearchItemsUsesCatalogSearchProviderWithCompatScope(t *testing.T) {
	libraryID := 7
	provider := &recordingCatalogSearchProvider{
		result: &catalog.CatalogSearchResult{
			Items: []*models.MediaItem{{
				ContentID: "movie-1",
				Type:      "movie",
				Title:     "Dune",
			}},
			Total:   12,
			HasMore: true,
		},
	}
	svc := &directContentService{
		searchProvider: provider,
		accessFilter: func(context.Context, int, string) catalog.AccessFilter {
			return catalog.AccessFilter{
				AllowedLibraryIDs:  []int{1, 2},
				ExcludedMediaTypes: []string{"ebook"},
				MaturityLimits:     access.MaturityLimits{MaxContentRating: "PG-13"},
			}
		},
	}

	result, err := svc.SearchItems(context.Background(), &Session{
		StreamAppUserID: 22,
		ProfileID:       "profile-1",
	}, SearchItemsOptions{
		Query:     "dune",
		Limit:     5,
		Offset:    10,
		LibraryID: &libraryID,
		SkipTotal: true,
	})
	if err != nil {
		t.Fatalf("SearchItems error: %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(provider.requests))
	}
	req := provider.requests[0]
	if req.Query != "dune" || req.Limit != 5 || req.Offset != 10 || !req.SkipTotal {
		t.Fatalf("provider request shape = %#v", req)
	}
	if want := []string{"movie", "series", "episode"}; !slices.Equal(req.ItemTypes, want) {
		t.Fatalf("ItemTypes = %#v, want %#v", req.ItemTypes, want)
	}
	if req.Access.PresentationLibraryID == nil || *req.Access.PresentationLibraryID != libraryID {
		t.Fatalf("PresentationLibraryID = %#v, want %d", req.Access.PresentationLibraryID, libraryID)
	}
	for _, mediaType := range []string{"ebook", "audiobook", "podcast"} {
		if !slices.Contains(req.Access.ExcludedMediaTypes, mediaType) {
			t.Fatalf("ExcludedMediaTypes = %#v, missing %q", req.Access.ExcludedMediaTypes, mediaType)
		}
	}
	if result.Total != 12 || !result.HasMore || len(result.Items) != 1 || result.Items[0].Title != "Dune" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBrowseItems_FillsLargeJellyfinLimitInSingleCatalogPage(t *testing.T) {
	provider := newProgressCountingStoreProvider()
	browse := &stubBrowseSource{
		items: makeBrowseTestMediaItems(400),
		total: 400,
	}
	svc := newDirectContentServiceForTest(browse, provider)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	params := url.Values{}
	params.Set("limit", "250")
	params.Set("offset", "10")
	params.Set("include_total", "true")

	result, err := svc.BrowseItems(context.Background(), session, params)
	if err != nil {
		t.Fatalf("BrowseItems returned error: %v", err)
	}
	if got, want := len(result.Items), 250; got != want {
		t.Fatalf("items length = %d, want %d", got, want)
	}
	if result.Total != 400 {
		t.Fatalf("Total = %d, want 400", result.Total)
	}
	if !result.HasMore {
		t.Fatal("HasMore = false, want true")
	}
	if got, want := len(browse.calls), 1; got != want {
		t.Fatalf("BrowsePage calls = %d, want %d", got, want)
	}

	call := browse.calls[0]
	if call.filters.Limit != 250 {
		t.Fatalf("Limit = %d, want 250", call.filters.Limit)
	}
	if call.filters.MaxLimit != compatBrowseMaxLimit {
		t.Fatalf("MaxLimit = %d, want %d", call.filters.MaxLimit, compatBrowseMaxLimit)
	}
	if call.filters.Offset != 10 {
		t.Fatalf("Offset = %d, want 10", call.filters.Offset)
	}
	if !call.includeTotal {
		t.Fatal("includeTotal = false, want true")
	}
}

func makeBrowseTestMediaItems(count int) []*models.MediaItem {
	items := make([]*models.MediaItem, 0, count)
	for i := range count {
		items = append(items, &models.MediaItem{
			ContentID: fmt.Sprintf("movie-%03d", i),
			Type:      "movie",
			Title:     fmt.Sprintf("Movie %03d", i),
		})
	}
	return items
}

// seriesRollupCountingStore layers userstore.SeriesEpisodeRollupStore on top
// of the panic-stub store: when the SQL rollup capability is present, the
// chunked ListProgressByMediaItems path must not run at all.
type seriesRollupCountingStore struct {
	*progressCountingStore
	counts            map[string]userstore.SeriesWatchCounts
	seasonCounts      map[int]userstore.SeriesWatchCounts
	err               error
	rollupCalls       int
	seasonRollupCalls int
}

func (s *seriesRollupCountingStore) SeriesEpisodeWatchCounts(_ context.Context, _ string, seriesIDs []string) (map[string]userstore.SeriesWatchCounts, error) {
	s.rollupCalls++
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]userstore.SeriesWatchCounts, len(seriesIDs))
	for _, id := range seriesIDs {
		if c, ok := s.counts[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (s *seriesRollupCountingStore) SeriesSeasonWatchCounts(context.Context, string, string) (map[int]userstore.SeriesWatchCounts, error) {
	s.seasonRollupCalls++
	if s.err != nil {
		return nil, s.err
	}
	return maps.Clone(s.seasonCounts), nil
}

func (s *seriesRollupCountingStore) SeasonEpisodeWatchCounts(context.Context, string, []string) (map[string]userstore.SeriesWatchCounts, error) {
	return map[string]userstore.SeriesWatchCounts{}, nil
}

// TestEnrichSeriesUserDataRollupFailure: a failed rollup query falls back to
// the per-episode rollup while the request is live. Once the request is
// canceled the fallback would fail on the same context, so it does not run,
// and only a failure that is not the cancellation itself logs a warning.
func TestEnrichSeriesUserDataRollupFailure(t *testing.T) {
	rollupErr := errors.New("rollup unavailable")
	for _, tc := range []struct {
		name         string
		cancel       bool
		err          error
		wantFallback bool
		wantWarn     bool
	}{
		{name: "live request", err: rollupErr, wantFallback: true, wantWarn: true},
		{name: "canceled request", cancel: true, err: context.Canceled},
		{name: "real failure after cancel", cancel: true, err: rollupErr, wantWarn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			store := &seriesRollupCountingStore{progressCountingStore: &progressCountingStore{}, err: tc.err}
			episodes := &seriesEpisodeSource{bySeries: map[string][]*models.Episode{"series-1": {{ContentID: "ep-1"}}}}
			svc := &directContentService{
				episodeRepo:   episodes,
				storeProvider: &completedProgressStoreProvider{store: store},
			}
			ctx, cancel := context.WithCancel(t.Context())
			if tc.cancel {
				cancel()
			}
			defer cancel()

			items := []upstreamListItem{{ContentID: "series-1", Type: "series", Title: "Show"}}
			svc.EnrichSeriesUserData(ctx, &Session{StreamAppUserID: 1, ProfileID: "profile-1"}, items)

			ran := episodes.listCalls > 0 || store.listProgressCalls > 0
			if ran != tc.wantFallback {
				t.Fatalf("fallback ran = %v (episode lists %d, progress lists %d), want %v",
					ran, episodes.listCalls, store.listProgressCalls, tc.wantFallback)
			}
			if warned := logs.Len() > 0; warned != tc.wantWarn {
				t.Fatalf("logged at warning or above = %v, want %v: %s", warned, tc.wantWarn, logs.String())
			}
		})
	}
}

func (s *progressCountingStore) LatestHistoryIDs(context.Context, string, map[string][]string) (map[string]string, error) {
	return nil, nil
}

func (s *seriesRollupCountingStore) SeriesLatestViewedAt(context.Context, string, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

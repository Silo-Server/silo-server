package jellycompat

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestMediaItemToListItemUsesMovieReleaseDateForPremiereDate(t *testing.T) {
	releaseDate := "2026-02-13"
	item := mediaItemToListItem(&models.MediaItem{
		ContentID:   "movie-1",
		Type:        "movie",
		Title:       "Future Movie",
		Year:        2026,
		ReleaseDate: &releaseDate,
	})

	if item.AirDate != releaseDate {
		t.Fatalf("got AirDate %q, want %q", item.AirDate, releaseDate)
	}
}

func TestItemDetailToUpstreamUsesMovieReleaseDateForPremiereDate(t *testing.T) {
	releaseDate := "2026-02-13"
	detail := itemDetailToUpstream(&catalog.ItemDetail{
		ContentID:   "movie-1",
		Type:        "movie",
		Title:       "Future Movie",
		Year:        2026,
		ReleaseDate: &releaseDate,
	})

	if detail.AirDate == nil || *detail.AirDate != releaseDate {
		t.Fatalf("got AirDate %v, want %q", detail.AirDate, releaseDate)
	}
}

func TestItemEtagIncludesPremiereDate(t *testing.T) {
	base := upstreamListItem{
		ContentID: "movie-1",
		Title:     "Future Movie",
		Year:      2026,
		AirDate:   "2026-02-13",
	}
	updated := base
	updated.AirDate = "2026-03-01"

	if itemEtag(base) == itemEtag(updated) {
		t.Fatal("expected date changes to alter the compat item etag")
	}
}

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

func TestPresignListItemsBatchResolvesImages(t *testing.T) {
	resolver := &countingCompatImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	svc := &directContentService{detailSvc: detailSvc}

	items := []upstreamListItem{
		{
			ContentID:   "movie-1",
			PosterURL:   "plug://poster-1",
			BackdropURL: "plug://backdrop-1",
			LogoURL:     "plug://logo-1",
		},
		{
			ContentID: "movie-2",
			PosterURL: "plug://poster-2",
			StillURL:  "plug://still-2",
		},
	}

	svc.presignListItems(context.Background(), items)

	if resolver.singleCalls != 0 {
		t.Fatalf("single image resolver calls = %d, want 0", resolver.singleCalls)
	}
	if resolver.batchCalls != 4 {
		t.Fatalf("batch image resolver calls = %d, want 4", resolver.batchCalls)
	}
	for _, variant := range resolver.variants {
		if variant != "card" {
			t.Fatalf("batch variant = %q, want card", variant)
		}
	}
	for _, path := range []string{"plug://poster-1", "plug://poster-2", "plug://backdrop-1", "plug://logo-1", "plug://still-2"} {
		if !slices.Contains(resolver.paths, path) {
			t.Fatalf("batch paths %v missing %q", resolver.paths, path)
		}
	}
	if items[0].PosterPath != "plug://poster-1" {
		t.Fatalf("PosterPath = %q, want original path", items[0].PosterPath)
	}
	if got := items[0].PosterURL; got != "batch:card:plug://poster-1" {
		t.Fatalf("PosterURL = %q", got)
	}
	if got := items[0].BackdropURL; got != "batch:card:plug://backdrop-1" {
		t.Fatalf("BackdropURL = %q", got)
	}
	if got := items[1].StillURL; got != "batch:card:plug://still-2" {
		t.Fatalf("StillURL = %q", got)
	}
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
// ListProgressByMediaItems. Other methods panic so we catch unexpected use.
type progressCountingStore struct {
	forUserCalls           int
	listProgressCalls      int
	lastListedMediaItemIDs []string
}

func (s *progressCountingStore) ListProgressByMediaItems(_ context.Context, _ string, mediaItemIDs []string) (map[string]userstore.WatchProgress, error) {
	s.listProgressCalls++
	s.lastListedMediaItemIDs = mediaItemIDs
	return map[string]userstore.WatchProgress{}, nil
}

// Remaining UserStore methods panic — the test should not exercise them.
func (s *progressCountingStore) CreateProfile(context.Context, userstore.Profile) error {
	panic("unused")
}
func (s *progressCountingStore) GetProfile(context.Context, string) (*userstore.Profile, error) {
	panic("unused")
}
func (s *progressCountingStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	panic("unused")
}
func (s *progressCountingStore) UpdateProfile(context.Context, string, userstore.UpdateProfileInput) error {
	panic("unused")
}
func (s *progressCountingStore) DeleteProfile(context.Context, string) error { panic("unused") }
func (s *progressCountingStore) VerifyPIN(context.Context, string, string) (bool, error) {
	panic("unused")
}
func (s *progressCountingStore) UpdateProgress(context.Context, string, string, float64, float64, userstore.ProgressThresholds) error {
	panic("unused")
}
func (s *progressCountingStore) SetProgress(context.Context, string, string, float64, float64, userstore.ProgressThresholds) error {
	panic("unused")
}
func (s *progressCountingStore) SetProgressAt(context.Context, string, string, float64, float64, bool, time.Time) error {
	panic("unused")
}
func (s *progressCountingStore) SetProgressIfNewer(context.Context, string, string, float64, float64, bool, time.Time) (bool, error) {
	panic("unused")
}
func (s *progressCountingStore) UpdateProgressHints(context.Context, string, string, userstore.VersionHints) error {
	panic("unused")
}
func (s *progressCountingStore) MarkWatched(context.Context, string, string, float64) error {
	panic("unused")
}
func (s *progressCountingStore) MarkProgressBatch(context.Context, string, []string, time.Time) error {
	panic("unused")
}
func (s *progressCountingStore) ClearProgressBatch(context.Context, string, []string, time.Time) error {
	panic("unused")
}
func (s *progressCountingStore) ClearProgress(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) GetProgress(context.Context, string, string) (*userstore.WatchProgress, error) {
	panic("unused")
}
func (s *progressCountingStore) ListProgress(context.Context, string, string, int, int) ([]userstore.WatchProgress, error) {
	panic("unused")
}
func (s *progressCountingStore) ListProgressFiltered(context.Context, string, string, []string, *int, int, int) ([]userstore.WatchProgress, error) {
	panic("unused")
}
func (s *progressCountingStore) ListProgressSince(context.Context, string, string) ([]userstore.WatchProgress, string, error) {
	panic("unused")
}
func (s *progressCountingStore) AddHistory(context.Context, userstore.WatchHistoryEntry) error {
	panic("unused")
}
func (s *progressCountingStore) AddHistoryIfMissing(context.Context, userstore.WatchHistoryEntry) (bool, error) {
	panic("unused")
}
func (s *progressCountingStore) ListHistory(context.Context, string, int, int) ([]userstore.WatchHistoryEntry, error) {
	panic("unused")
}
func (s *progressCountingStore) ListCompletedHistory(context.Context, userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	panic("unused")
}
func (s *progressCountingStore) ListCompletedHistoryItems(context.Context, userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	return nil, nil
}
func (s *progressCountingStore) RemoveHistoryItems(context.Context, string, []string, time.Time) error {
	panic("unused")
}
func (s *progressCountingStore) DeleteHistoryBySource(context.Context, string, []string, userstore.WatchHistorySource) error {
	panic("unused")
}
func (s *progressCountingStore) ListHomeDismissals(context.Context, string, string) ([]userstore.HomeItemDismissal, error) {
	panic("unused")
}
func (s *progressCountingStore) UpsertHomeDismissal(context.Context, userstore.HomeItemDismissal) error {
	panic("unused")
}
func (s *progressCountingStore) DeleteHomeDismissal(context.Context, string, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) AddFavorite(context.Context, string, string) error { panic("unused") }
func (s *progressCountingStore) AddFavoriteAt(context.Context, string, string, time.Time) error {
	panic("unused")
}
func (s *progressCountingStore) RemoveFavorite(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) ListFavorites(context.Context, string, int, int) ([]userstore.Favorite, error) {
	panic("unused")
}
func (s *progressCountingStore) ListFavoritesByMediaItems(context.Context, string, []string) (map[string]bool, error) {
	panic("unused")
}
func (s *progressCountingStore) IsFavorite(context.Context, string, string) (bool, error) {
	panic("unused")
}
func (s *progressCountingStore) AddToWatchlist(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) AddToWatchlistAt(context.Context, string, string, time.Time) (bool, error) {
	panic("unused")
}
func (s *progressCountingStore) RemoveWatchedFromWatchlist(context.Context, string) (bool, error) {
	return true, nil
}
func (s *progressCountingStore) RemoveFromWatchlist(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) ReplaceWatchlistOrder(context.Context, string, []string) error {
	panic("unused")
}
func (s *progressCountingStore) ListWatchlist(context.Context, string, int, int) ([]userstore.WatchlistEntry, error) {
	panic("unused")
}
func (s *progressCountingStore) ListWatchlistByMediaItems(context.Context, string, []string) (map[string]bool, error) {
	panic("unused")
}
func (s *progressCountingStore) InWatchlist(context.Context, string, string) (bool, error) {
	panic("unused")
}
func (s *progressCountingStore) CreateCollection(context.Context, userstore.CreateCollectionInput) (*userstore.Collection, error) {
	panic("unused")
}
func (s *progressCountingStore) GetCollection(context.Context, string) (*userstore.Collection, error) {
	panic("unused")
}
func (s *progressCountingStore) ListCollections(context.Context, string) ([]userstore.Collection, error) {
	panic("unused")
}
func (s *progressCountingStore) UpdateCollection(context.Context, userstore.UpdateCollectionInput) error {
	panic("unused")
}
func (s *progressCountingStore) DeleteCollection(context.Context, string) error { panic("unused") }
func (s *progressCountingStore) AddCollectionItem(context.Context, string, string, int) error {
	panic("unused")
}
func (s *progressCountingStore) RemoveCollectionItem(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) ListCollectionItems(context.Context, string) ([]userstore.CollectionItem, error) {
	panic("unused")
}
func (s *progressCountingStore) ReplaceCollectionItems(context.Context, string, []userstore.CollectionItemReplacement) error {
	panic("unused")
}
func (s *progressCountingStore) ReorderCollectionItems(context.Context, string, []string) error {
	panic("unused")
}
func (s *progressCountingStore) ReorderCollections(context.Context, string, *string, []string) error {
	panic("unused")
}
func (s *progressCountingStore) UpdateCollectionSyncState(context.Context, userstore.UpdateCollectionSyncStateInput) error {
	panic("unused")
}
func (s *progressCountingStore) ListCollectionGroups(context.Context) ([]userstore.CollectionGroup, error) {
	panic("unused")
}
func (s *progressCountingStore) EnsureCollectionGroup(context.Context, string) error { panic("unused") }
func (s *progressCountingStore) CreateCollectionGroup(context.Context, string, string, userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	panic("unused")
}
func (s *progressCountingStore) UpdateCollectionGroup(context.Context, string, *string, *string, *userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	panic("unused")
}
func (s *progressCountingStore) DeleteCollectionGroup(context.Context, string) error {
	panic("unused")
}
func (s *progressCountingStore) ReorderCollectionGroups(context.Context, []string) error {
	panic("unused")
}
func (s *progressCountingStore) ListSectionOverrides(context.Context, string, string, string) ([]userstore.SectionOverride, error) {
	panic("unused")
}
func (s *progressCountingStore) SaveSectionOverrides(context.Context, string, string, string, []userstore.SectionOverride) error {
	panic("unused")
}
func (s *progressCountingStore) ResetSectionOverrides(context.Context, string, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) GetSetting(context.Context, string) (string, error) {
	panic("unused")
}
func (s *progressCountingStore) SetSetting(context.Context, string, string) error { panic("unused") }
func (s *progressCountingStore) DeleteSetting(context.Context, string) error      { panic("unused") }
func (s *progressCountingStore) ListSettings(context.Context) ([]userstore.SettingEntry, error) {
	panic("unused")
}
func (s *progressCountingStore) GetDeviceSetting(context.Context, string, string, string) (*userstore.DeviceSettingEntry, error) {
	panic("unused")
}
func (s *progressCountingStore) SetDeviceSetting(context.Context, userstore.DeviceSettingEntry) error {
	panic("unused")
}
func (s *progressCountingStore) DeleteDeviceSetting(context.Context, string, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) DeleteAllDeviceSettings(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) DeleteDeviceSettingsByKey(context.Context, string) error {
	panic("unused")
}
func (s *progressCountingStore) ListDeviceSettings(context.Context, string) ([]userstore.DeviceSettingEntry, error) {
	panic("unused")
}
func (s *progressCountingStore) ListAllDeviceSettings(context.Context) ([]userstore.DeviceSettingEntry, error) {
	panic("unused")
}
func (s *progressCountingStore) SetSubtitlePreference(context.Context, userstore.SubtitlePreference) error {
	panic("unused")
}
func (s *progressCountingStore) GetSubtitlePreference(context.Context, string, string) (*userstore.SubtitlePreference, error) {
	panic("unused")
}
func (s *progressCountingStore) DeleteSubtitlePreference(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) SetAudioPreference(context.Context, userstore.AudioPreference) error {
	panic("unused")
}
func (s *progressCountingStore) GetAudioPreference(context.Context, string, string) (*userstore.AudioPreference, error) {
	panic("unused")
}
func (s *progressCountingStore) DeleteAudioPreference(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) SetSeriesPlaybackPreference(context.Context, userstore.SeriesPlaybackPreference) error {
	panic("unused")
}
func (s *progressCountingStore) GetSeriesPlaybackPreference(context.Context, string, string) (*userstore.SeriesPlaybackPreference, error) {
	panic("unused")
}
func (s *progressCountingStore) DeleteSeriesPlaybackPreference(context.Context, string, string) error {
	panic("unused")
}
func (s *progressCountingStore) GetLibraryPlaybackPreference(context.Context, string, int) (*userstore.LibraryPlaybackPreference, error) {
	panic("unused")
}
func (s *progressCountingStore) ListLibraryPlaybackPreferences(context.Context, string) ([]userstore.LibraryPlaybackPreference, error) {
	panic("unused")
}
func (s *progressCountingStore) UpsertLibraryPlaybackPreference(context.Context, userstore.LibraryPlaybackPreference) error {
	panic("unused")
}
func (s *progressCountingStore) DeleteLibraryPlaybackPreference(context.Context, string, int) error {
	panic("unused")
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

func TestBrowseItems_RecentlyAddedNoParentFansOutAcrossLibraries(t *testing.T) {
	browse := &stubBrowseSource{items: makeBrowseTestMediaItems(10), total: 10}
	svc := newDirectContentServiceForTest(browse, nil)
	svc.folderRepo = &stubFolderSource{
		enabled: []*models.MediaFolder{
			{ID: 1, Name: "Movies", Type: "movie"},
			{ID: 3, Name: "TV", Type: "series"},
		},
	}

	session := &Session{StreamAppUserID: 1, ProfileID: "p1"}
	params := url.Values{}
	params.Set("limit", "5")
	params.Set("sort", "recently_added")

	if _, err := svc.BrowseItems(context.Background(), session, params); err != nil {
		t.Fatalf("BrowseItems: %v", err)
	}
	if got := len(browse.crossLibraryCall); got != 1 {
		t.Fatalf("cross-library calls = %d, want 1", got)
	}
	if got := len(browse.calls); got != 0 {
		t.Fatalf("BrowsePage calls = %d, want 0 (fast path should not use it)", got)
	}
	if got := browse.crossLibraryCall[0].libraryIDs; len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("library IDs = %v, want [1 3]", got)
	}
}

func TestBrowseItems_RecentlyAddedWithOffsetFallsBackToBrowsePage(t *testing.T) {
	browse := &stubBrowseSource{items: makeBrowseTestMediaItems(40), total: 40}
	svc := newDirectContentServiceForTest(browse, nil)
	svc.folderRepo = &stubFolderSource{
		enabled: []*models.MediaFolder{
			{ID: 1, Name: "Movies", Type: "movie"},
			{ID: 3, Name: "TV", Type: "series"},
		},
	}

	session := &Session{StreamAppUserID: 1, ProfileID: "p1"}
	params := url.Values{}
	params.Set("limit", "5")
	params.Set("offset", "5")
	params.Set("sort", "recently_added")

	if _, err := svc.BrowseItems(context.Background(), session, params); err != nil {
		t.Fatalf("BrowseItems: %v", err)
	}
	if got := len(browse.crossLibraryCall); got != 0 {
		t.Fatalf("cross-library calls = %d, want 0 for offset>0", got)
	}
	if got := len(browse.calls); got == 0 {
		t.Fatal("BrowsePage calls = 0, want offset>0 to fall back to BrowsePage")
	}
}

func TestBrowseItems_RecentlyAddedSingleLibraryUsesBrowsePage(t *testing.T) {
	browse := &stubBrowseSource{items: makeBrowseTestMediaItems(10), total: 10}
	svc := newDirectContentServiceForTest(browse, nil)
	svc.folderRepo = &stubFolderSource{
		enabled: []*models.MediaFolder{{ID: 1, Name: "Movies", Type: "movie"}},
	}

	session := &Session{StreamAppUserID: 1, ProfileID: "p1"}
	params := url.Values{}
	params.Set("limit", "5")
	params.Set("sort", "recently_added")

	if _, err := svc.BrowseItems(context.Background(), session, params); err != nil {
		t.Fatalf("BrowseItems: %v", err)
	}
	if got := len(browse.crossLibraryCall); got != 0 {
		t.Fatalf("cross-library calls = %d, want 0 with a single library", got)
	}
	if got := len(browse.calls); got == 0 {
		t.Fatal("BrowsePage calls = 0, want single-library path to use BrowsePage")
	}
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

type recordingItemAccessSource struct {
	searchQueries []string
	searchTypes   [][]string
	searchLimits  []int
	searchOffsets []int
	items         []*models.MediaItem
	total         int
}

func (s *recordingItemAccessSource) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return nil
}

func (s *recordingItemAccessSource) Search(_ context.Context, query string, itemTypes []string, limit, offset int, _ catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	s.searchQueries = append(s.searchQueries, query)
	s.searchTypes = append(s.searchTypes, append([]string(nil), itemTypes...))
	s.searchLimits = append(s.searchLimits, limit)
	s.searchOffsets = append(s.searchOffsets, offset)
	return append([]*models.MediaItem(nil), s.items...), s.total, nil
}

func (s *recordingItemAccessSource) GetByIDs(context.Context, []string) ([]*models.MediaItem, error) {
	return nil, nil
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
				MaxContentRating:   "PG-13",
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

func TestSearchItemsFallsBackToItemRepoWhenProviderMissing(t *testing.T) {
	itemRepo := &recordingItemAccessSource{
		items: []*models.MediaItem{{
			ContentID: "movie-1",
			Type:      "movie",
			Title:     "Fallback",
		}},
		total: 3,
	}
	svc := &directContentService{itemRepo: itemRepo}

	result, err := svc.SearchItems(context.Background(), &Session{}, SearchItemsOptions{
		Query:     "fallback",
		ItemTypes: []string{"MusicAlbum"},
		Limit:     2,
		Offset:    1,
	})
	if err != nil {
		t.Fatalf("SearchItems error: %v", err)
	}
	if len(itemRepo.searchQueries) != 1 || itemRepo.searchQueries[0] != "fallback" {
		t.Fatalf("search queries = %#v", itemRepo.searchQueries)
	}
	if want := []string{compatNoMatchType}; !slices.Equal(itemRepo.searchTypes[0], want) {
		t.Fatalf("search types = %#v, want %#v", itemRepo.searchTypes[0], want)
	}
	if result.Total != 3 || !result.HasMore || len(result.Items) != 1 || result.Items[0].Title != "Fallback" {
		t.Fatalf("result = %#v", result)
	}
}

// TestBrowseItems_DoesNotFetchProgressWhenNoPlayedFilter verifies that
// BrowseItems does NOT call ListProgressByMediaItems on the user store when
// the is_played filter is empty. The handler-level resolveUserStateForContentIDs
// owns user-data enrichment for the wire response; the inner per-iteration
// fetch was duplicate work (audit 2026-05-01 §2.8).
//
// We use a stub browseSource that returns a non-empty result, so the buggy
// code path would actually reach ForUser/ListProgressByMediaItems on the
// counting store and fail this assertion. The fixed code only enriches when
// filtering by played status, so neither method is called.
func TestBrowseItems_DoesNotFetchProgressWhenNoPlayedFilter(t *testing.T) {
	provider := newProgressCountingStoreProvider()
	browse := &stubBrowseSource{
		items: []*models.MediaItem{
			{ContentID: "movie-1", Type: "movie", Title: "A"},
			{ContentID: "movie-2", Type: "movie", Title: "B"},
		},
		total: 2,
	}
	svc := newDirectContentServiceForTest(browse, provider)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	params := url.Values{}
	params.Set("limit", "24")

	if _, err := svc.BrowseItems(context.Background(), session, params); err != nil {
		t.Fatalf("BrowseItems returned error: %v", err)
	}
	if provider.store.listProgressCalls != 0 {
		t.Fatalf("BrowseItems must not call ListProgressByMediaItems when is_played filter is empty; got %d calls",
			provider.store.listProgressCalls)
	}
}

// TestBrowseItems_FetchesProgressWhenPlayedFilterSet verifies the
// load-bearing behavior preserved by the fix: when is_played filter is set,
// BrowseItems still enriches user data so the loop can filter by played
// status. Without enrichment, is_played=true would always return zero
// items and is_played=false would always return everything.
func TestBrowseItems_FetchesProgressWhenPlayedFilterSet(t *testing.T) {
	provider := newProgressCountingStoreProvider()
	browse := &stubBrowseSource{
		items: []*models.MediaItem{
			{ContentID: "movie-1", Type: "movie", Title: "A"},
			{ContentID: "movie-2", Type: "movie", Title: "B"},
		},
		total: 2,
	}
	svc := newDirectContentServiceForTest(browse, provider)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	params := url.Values{}
	params.Set("limit", "24")
	params.Set("is_played", "true")

	if _, err := svc.BrowseItems(context.Background(), session, params); err != nil {
		t.Fatalf("BrowseItems returned error: %v", err)
	}
	if provider.store.listProgressCalls < 1 {
		t.Fatalf("BrowseItems with is_played filter must fetch progress; got %d calls",
			provider.store.listProgressCalls)
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

func TestBrowseItems_HonorsIncludeTotalFalse(t *testing.T) {
	browse := &stubBrowseSource{
		items: makeBrowseTestMediaItems(300),
		total: 300,
	}
	svc := newDirectContentServiceForTest(browse, nil)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	params := url.Values{}
	params.Set("limit", "150")
	params.Set("include_total", "false")

	result, err := svc.BrowseItems(context.Background(), session, params)
	if err != nil {
		t.Fatalf("BrowseItems returned error: %v", err)
	}
	if got, want := len(result.Items), 150; got != want {
		t.Fatalf("items length = %d, want %d", got, want)
	}
	if result.Total != 0 {
		t.Fatalf("Total = %d, want 0 when include_total=false", result.Total)
	}
	if got, want := len(browse.calls), 1; got != want {
		t.Fatalf("BrowsePage calls = %d, want %d", got, want)
	}
	for i, call := range browse.calls {
		if call.includeTotal {
			t.Fatalf("call %d includeTotal = true, want false", i)
		}
	}
}

// TestEnrichListItemsUserData_BatchesIntoSingleFetch verifies that
// enrichListItemsUserData makes exactly one batched ListProgressByMediaItems
// call regardless of batch size — not N+1 per-item fetches.
func TestEnrichListItemsUserData_BatchesIntoSingleFetch(t *testing.T) {
	provider := newProgressCountingStoreProvider()
	svc := newDirectContentServiceForTest(nil, provider)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	batch := []upstreamListItem{
		{ContentID: "movie-1"},
		{ContentID: "movie-2"},
		{ContentID: "movie-3"},
	}
	svc.enrichListItemsUserData(context.Background(), session, batch)

	if provider.store.listProgressCalls != 1 {
		t.Fatalf("enrichListItemsUserData should batch into 1 ListProgressByMediaItems call; got %d",
			provider.store.listProgressCalls)
	}
	if got, want := len(provider.store.lastListedMediaItemIDs), 3; got != want {
		t.Fatalf("ListProgressByMediaItems should receive all %d ids in one batch; got %d",
			want, got)
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

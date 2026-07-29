package metadata

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	staleRecoveryContentID = "legacy-item-1"
	staleRecoveryTitle     = "Recovered Movie"
	staleRecoveryIMDbID    = "tt1234567"
)

type fakeProviderIDRepo struct {
	mu          sync.Mutex
	byContentID map[string][]*models.MediaItemProviderID
	lastReplace map[string]map[string]string
}

func newFakeProviderIDRepo() *fakeProviderIDRepo {
	return &fakeProviderIDRepo{
		byContentID: make(map[string][]*models.MediaItemProviderID),
		lastReplace: make(map[string]map[string]string),
	}
}

func (r *fakeProviderIDRepo) GetByContentID(_ context.Context, contentID string) ([]*models.MediaItemProviderID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := r.byContentID[contentID]
	out := make([]*models.MediaItemProviderID, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		cp := *row
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeProviderIDRepo) ReplaceByContentID(_ context.Context, contentID string, providerIDs map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make(map[string]string, len(providerIDs))
	for k, v := range providerIDs {
		if isEphemeralProviderIDKey(k) {
			continue
		}
		cp[k] = v
	}
	r.lastReplace[contentID] = cp
	return nil
}

func (r *fakeProviderIDRepo) FindContentIDByProviderIDs(_ context.Context, providerIDs map[string]string, itemType, excludeContentID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for contentID, rows := range r.byContentID {
		if contentID == excludeContentID {
			continue
		}
		for _, row := range rows {
			if row == nil || row.ProviderID == "" {
				continue
			}
			if itemType != "" && row.ItemType != "" && row.ItemType != itemType {
				continue
			}
			if isEphemeralProviderIDKey(row.Provider) {
				continue
			}
			if providerIDs[row.Provider] == row.ProviderID {
				return contentID, nil
			}
		}
	}
	return "", nil
}

func (r *fakeProviderIDRepo) set(contentID string, ids ...*models.MediaItemProviderID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]*models.MediaItemProviderID, 0, len(ids))
	for _, id := range ids {
		if id == nil {
			continue
		}
		row := *id
		cp = append(cp, &row)
	}
	r.byContentID[contentID] = cp
}

type fakeStaleIDRepo struct {
	mu          sync.Mutex
	byContentID map[string][]*models.StaleMediaID
}

func newFakeStaleIDRepo() *fakeStaleIDRepo {
	return &fakeStaleIDRepo{byContentID: make(map[string][]*models.StaleMediaID)}
}

func (r *fakeStaleIDRepo) GetByContentID(_ context.Context, contentID string) ([]*models.StaleMediaID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := r.byContentID[contentID]
	out := make([]*models.StaleMediaID, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		cp := *row
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeStaleIDRepo) Upsert(_ context.Context, contentID, provider, providerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.byContentID[contentID] {
		if row == nil || row.Provider != provider || row.ProviderID != providerID {
			continue
		}
		return nil
	}
	r.byContentID[contentID] = append(r.byContentID[contentID], &models.StaleMediaID{
		ContentID:  contentID,
		Provider:   provider,
		ProviderID: providerID,
	})
	return nil
}

func (r *fakeStaleIDRepo) DeleteByContentID(_ context.Context, contentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byContentID, contentID)
	return nil
}

func (r *fakeStaleIDRepo) set(contentID string, ids ...*models.StaleMediaID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]*models.StaleMediaID, 0, len(ids))
	for _, id := range ids {
		if id == nil {
			continue
		}
		row := *id
		cp = append(cp, &row)
	}
	r.byContentID[contentID] = cp
}

type capturingMetadataProvider struct {
	mu       sync.Mutex
	lastReq  MetadataRequest
	response *MetadataResult
}

type searchMetadataProvider struct {
	slug           string
	searchResults  []SearchResult
	metadataResult *MetadataResult
	metadataCalls  int
}

type image404MetadataProvider struct {
	*searchMetadataProvider
}

type searchThen404MetadataProvider struct {
	slug          string
	searchResults []SearchResult
}

type capturingEpisodeBootstrapProvider struct {
	providerIDs map[string]string
}

func (p *image404MetadataProvider) GetImages(_ context.Context, _ ImageRequest) ([]RemoteImage, error) {
	return nil, errors.New(p.slug + ": HTTP 404: not found")
}

func (p *searchThen404MetadataProvider) Slug() string { return p.slug }

func (p *searchThen404MetadataProvider) Name() string { return p.slug }

func (p *searchThen404MetadataProvider) ForTypes() []string {
	return []string{anchoredItemTypeMovie, anchoredItemTypeSeries}
}

func (p *searchThen404MetadataProvider) Search(_ context.Context, _ SearchQuery) ([]SearchResult, error) {
	return append([]SearchResult(nil), p.searchResults...), nil
}

func (p *searchThen404MetadataProvider) GetMetadata(_ context.Context, _ MetadataRequest) (*MetadataResult, error) {
	return nil, errors.New(p.slug + ": HTTP 404: not found")
}

func (p *capturingEpisodeBootstrapProvider) Slug() string { return contentid.ProviderTVDB }

func (p *capturingEpisodeBootstrapProvider) Name() string { return "TVDB episodes" }

func (p *capturingEpisodeBootstrapProvider) ForTypes() []string {
	return []string{anchoredItemTypeSeries}
}

func (p *capturingEpisodeBootstrapProvider) GetSeasons(_ context.Context, req SeasonsRequest) ([]SeasonResult, error) {
	p.providerIDs = copyMap(req.ProviderIDs)
	return nil, nil
}

func (p *capturingEpisodeBootstrapProvider) GetEpisodes(context.Context, EpisodesRequest) ([]EpisodeResult, error) {
	return nil, nil
}

func (p *searchMetadataProvider) Slug() string { return p.slug }

func (p *searchMetadataProvider) Name() string { return p.slug }

func (p *searchMetadataProvider) ForTypes() []string {
	return []string{anchoredItemTypeMovie, anchoredItemTypeSeries}
}

func (p *searchMetadataProvider) Search(_ context.Context, _ SearchQuery) ([]SearchResult, error) {
	results := make([]SearchResult, len(p.searchResults))
	for i := range p.searchResults {
		results[i] = p.searchResults[i]
		results[i].ProviderIDs = copyMap(p.searchResults[i].ProviderIDs)
	}
	return results, nil
}

func (p *searchMetadataProvider) GetMetadata(_ context.Context, _ MetadataRequest) (*MetadataResult, error) {
	p.metadataCalls++
	if p.metadataResult == nil {
		return nil, nil
	}
	result := *p.metadataResult
	result.ProviderIDs = copyMap(p.metadataResult.ProviderIDs)
	return &result, nil
}

func (p *capturingMetadataProvider) Slug() string { return "capture" }

func (p *capturingMetadataProvider) Name() string { return "capture" }

func (p *capturingMetadataProvider) ForTypes() []string { return []string{"movie", "series"} }

func (p *capturingMetadataProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastReq = req
	if p.response != nil {
		cp := *p.response
		cp.ProviderIDs = copyMap(p.response.ProviderIDs)
		return &cp, nil
	}
	return &MetadataResult{HasMetadata: false}, nil
}

func (p *capturingMetadataProvider) lastRequest() MetadataRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastReq
}

func TestProcess_ScheduledRefreshReplacesRecordedStaleCurrentID(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: staleRecoveryContentID,
		Type:      anchoredItemTypeMovie,
		Title:     staleRecoveryTitle,
		Year:      2020,
		Status:    string(MatchOutcomeMatched),
		TmdbID:    "111",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(staleRecoveryContentID, &models.MediaItemProviderID{
		ContentID: staleRecoveryContentID, ItemType: anchoredItemTypeMovie,
		Provider: contentid.ProviderTMDB, ProviderID: "111",
	})
	h.service.providerIDRepo = providerRepo
	staleRepo := newFakeStaleIDRepo()
	staleRepo.set(staleRecoveryContentID, &models.StaleMediaID{
		ContentID: staleRecoveryContentID, Provider: contentid.ProviderTMDB, ProviderID: "111",
	})
	h.service.staleIDRepo = staleRepo

	tmdb := &searchMetadataProvider{
		slug: contentid.ProviderTMDB,
		searchResults: []SearchResult{{
			Name: staleRecoveryTitle, Year: 2020, Provider: contentid.ProviderTMDB,
			ProviderIDs: map[string]string{contentid.ProviderTMDB: "222", contentid.ProviderIMDB: staleRecoveryIMDbID},
		}},
		metadataResult: &MetadataResult{
			HasMetadata: true, Title: staleRecoveryTitle, Year: 2020,
			ProviderIDs: map[string]string{contentid.ProviderTMDB: "222", contentid.ProviderIMDB: staleRecoveryIMDbID},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: staleRecoveryContentID,
		Language:  "en",
		Mode:      ModeScheduledRefresh,
	}, []Provider{tmdb})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	providerRepo.mu.Lock()
	persisted := providerRepo.lastReplace[staleRecoveryContentID]
	providerRepo.mu.Unlock()
	if persisted[contentid.ProviderTMDB] != "222" {
		t.Fatalf("persisted tmdb id = %q, want replacement 222", persisted[contentid.ProviderTMDB])
	}
	if persisted[contentid.ProviderIMDB] != staleRecoveryIMDbID {
		t.Fatalf("persisted imdb id = %q, want %s", persisted[contentid.ProviderIMDB], staleRecoveryIMDbID)
	}
	if tmdb.metadataCalls != 1 {
		t.Fatalf("metadata calls = %d, want 1", tmdb.metadataCalls)
	}
}

func TestProcess_InitialMatchPreservesAggregatorProviderIdentity(t *testing.T) {
	const (
		aggregatorTitle  = "Aggregator Movie"
		aggregatorIMDbID = "tt7654321"
	)
	h := newTestHarness()
	provider := &searchMetadataProvider{
		slug: testMetaDBProvider,
		searchResults: []SearchResult{{
			Name: aggregatorTitle, Year: 2020, Provider: testMetaDBProvider,
			ProviderIDs: map[string]string{
				contentid.ProviderTMDB: "999",
				contentid.ProviderIMDB: aggregatorIMDbID,
			},
		}},
		metadataResult: &MetadataResult{
			HasMetadata: true, Title: aggregatorTitle, Year: 2020,
			ProviderIDs: map[string]string{
				contentid.ProviderTMDB: "999",
				contentid.ProviderIMDB: aggregatorIMDbID,
			},
		},
	}
	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{Title: aggregatorTitle, Year: 2020, Type: anchoredItemTypeMovie},
		Mode:  ModeInitialMatch,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.ContentID != "movie-tmdb-999" {
		t.Fatalf("result = %#v, want movie-tmdb-999", result)
	}
	item, err := h.itemRepo.GetByID(context.Background(), result.ContentID)
	if err != nil {
		t.Fatalf("load matched item: %v", err)
	}
	if item.TmdbID != "999" || item.ImdbID != aggregatorIMDbID {
		t.Fatalf("persisted ids = tmdb:%q imdb:%q", item.TmdbID, item.ImdbID)
	}
}

func TestProcess_ScheduledRefreshDoesNotReanchorRecordedStaleContentID(t *testing.T) {
	const (
		contentID = "movie-tmdb-777"
		imdbID    = "tt0000777"
	)
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: contentID, Type: anchoredItemTypeMovie, Title: "Stable Identity",
		Status: string(MatchOutcomeMatched), TmdbID: "777", ImdbID: imdbID,
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(contentID,
		&models.MediaItemProviderID{ContentID: contentID, ItemType: anchoredItemTypeMovie, Provider: contentid.ProviderTMDB, ProviderID: "777"},
		&models.MediaItemProviderID{ContentID: contentID, ItemType: anchoredItemTypeMovie, Provider: contentid.ProviderIMDB, ProviderID: imdbID},
	)
	h.service.providerIDRepo = providerRepo
	staleRepo := newFakeStaleIDRepo()
	staleRepo.set(contentID, &models.StaleMediaID{
		ContentID: contentID, Provider: contentid.ProviderTMDB, ProviderID: "777",
	})
	h.service.staleIDRepo = staleRepo
	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Stable Identity",
		ProviderIDs: map[string]string{contentid.ProviderIMDB: imdbID},
	}}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: contentID, Language: "en", Mode: ModeScheduledRefresh,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.ContentID != contentID {
		t.Fatalf("result content id = %#v, want %s preserved", result, contentID)
	}
}

func TestProcess_ScheduledRefreshDoesNotRestoreProviderIDThat404sDuringSameRun(t *testing.T) {
	const (
		itemID   = "legacy-item-new-404"
		badTMDB  = "333"
		goodTVDB = "444"
	)
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: itemID,
		Type:      anchoredItemTypeSeries,
		Title:     "Still Available",
		Status:    string(MatchOutcomeMatched),
		TmdbID:    badTMDB,
		TvdbID:    goodTVDB,
		Studios:   []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(itemID,
		&models.MediaItemProviderID{ContentID: itemID, ItemType: anchoredItemTypeSeries, Provider: contentid.ProviderTMDB, ProviderID: badTMDB},
		&models.MediaItemProviderID{ContentID: itemID, ItemType: anchoredItemTypeSeries, Provider: contentid.ProviderTVDB, ProviderID: goodTVDB},
	)
	h.service.providerIDRepo = providerRepo
	h.service.staleIDRepo = newFakeStaleIDRepo()

	tvdb := &searchMetadataProvider{
		slug: contentid.ProviderTVDB,
		metadataResult: &MetadataResult{
			HasMetadata: true,
			Title:       "Still Available",
			ProviderIDs: map[string]string{contentid.ProviderTVDB: goodTVDB},
		},
	}
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: itemID,
		Language:  "en",
		Mode:      ModeScheduledRefresh,
	}, []Provider{&notFoundMetadataProvider{slug: contentid.ProviderTMDB}, tvdb})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	providerRepo.mu.Lock()
	persisted := providerRepo.lastReplace[itemID]
	providerRepo.mu.Unlock()
	if persisted[contentid.ProviderTMDB] != "" {
		t.Fatalf("same-run 404 tmdb id was restored: %#v", persisted)
	}
	if persisted[contentid.ProviderTVDB] != goodTVDB {
		t.Fatalf("persisted tvdb id = %q, want %s", persisted[contentid.ProviderTVDB], goodTVDB)
	}
}

func TestProcess_ScheduledRefreshSuppressesRecordedAndReplacement404Values(t *testing.T) {
	const (
		itemID          = "legacy-item-two-stale-values"
		oldTMDBID       = "111"
		replacementTMDB = "222"
		goodTVDBID      = "444"
		title           = "Two Bad IDs"
	)
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: itemID, Type: anchoredItemTypeSeries, Title: title, Year: 2020,
		Status: string(MatchOutcomeMatched), TmdbID: oldTMDBID,
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(itemID, &models.MediaItemProviderID{
		ContentID: itemID, ItemType: anchoredItemTypeSeries,
		Provider: contentid.ProviderTMDB, ProviderID: oldTMDBID,
	})
	h.service.providerIDRepo = providerRepo
	staleRepo := newFakeStaleIDRepo()
	staleRepo.set(itemID, &models.StaleMediaID{
		ContentID: itemID, Provider: contentid.ProviderTMDB, ProviderID: oldTMDBID,
	})
	h.service.staleIDRepo = staleRepo

	tmdb := &searchThen404MetadataProvider{
		slug: contentid.ProviderTMDB,
		searchResults: []SearchResult{{
			Name: title, Year: 2020, Provider: contentid.ProviderTMDB,
			ProviderIDs: map[string]string{contentid.ProviderTMDB: replacementTMDB},
		}},
	}
	tvdb := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: title,
		ProviderIDs: map[string]string{contentid.ProviderTVDB: goodTVDBID},
	}}
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: itemID, Language: "en", Mode: ModeScheduledRefresh,
	}, []Provider{tmdb, tvdb})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	providerRepo.mu.Lock()
	persisted := providerRepo.lastReplace[itemID]
	providerRepo.mu.Unlock()
	if persisted[contentid.ProviderTMDB] != "" {
		t.Fatalf("stale tmdb value was restored: %#v", persisted)
	}
	if persisted[contentid.ProviderTVDB] != goodTVDBID {
		t.Fatalf("persisted tvdb id = %q, want %s", persisted[contentid.ProviderTVDB], goodTVDBID)
	}
	staleRows, err := staleRepo.GetByContentID(ctx, itemID)
	if err != nil {
		t.Fatalf("load stale rows: %v", err)
	}
	staleTMDBIDs := make(map[string]bool)
	for _, row := range staleRows {
		if row != nil && row.Provider == contentid.ProviderTMDB {
			staleTMDBIDs[row.ProviderID] = true
		}
	}
	if !staleTMDBIDs[oldTMDBID] || !staleTMDBIDs[replacementTMDB] {
		t.Fatalf("stale tmdb IDs = %#v, want both %s and %s", staleTMDBIDs, oldTMDBID, replacementTMDB)
	}
}

func TestProcess_ScheduledRefreshPreservesProviderIDWhenOnlyImages404(t *testing.T) {
	const (
		itemID = "legacy-item-image-404"
		tmdbID = "555"
		title  = "Metadata Without Images"
	)
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: itemID,
		Type:      anchoredItemTypeMovie,
		Title:     title,
		Status:    string(MatchOutcomeMatched),
		TmdbID:    tmdbID,
		Studios:   []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(itemID, &models.MediaItemProviderID{
		ContentID: itemID, ItemType: anchoredItemTypeMovie,
		Provider: contentid.ProviderTMDB, ProviderID: tmdbID,
	})
	h.service.providerIDRepo = providerRepo
	h.service.staleIDRepo = newFakeStaleIDRepo()

	provider := &image404MetadataProvider{searchMetadataProvider: &searchMetadataProvider{
		slug: contentid.ProviderTMDB,
		metadataResult: &MetadataResult{
			HasMetadata: true,
			Title:       title,
			ProviderIDs: map[string]string{contentid.ProviderTMDB: tmdbID},
		},
	}}
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: itemID,
		Language:  "en",
		Mode:      ModeScheduledRefresh,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	providerRepo.mu.Lock()
	persisted := providerRepo.lastReplace[itemID]
	providerRepo.mu.Unlock()
	if persisted[contentid.ProviderTMDB] != tmdbID {
		t.Fatalf("image-phase 404 removed healthy tmdb id: %#v", persisted)
	}
	stale, err := h.service.staleIDRepo.GetByContentID(ctx, itemID)
	if err != nil {
		t.Fatalf("get stale ids: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("image-phase 404 recorded stale identity: %#v", stale)
	}
}

func TestProcess_IdentifyBootstrapsEpisodeProviderFromDetailCrossReference(t *testing.T) {
	const (
		itemID = "series-bootstrap-detail-id"
		tmdbID = "123"
		tvdbID = "456"
	)
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: itemID, Type: anchoredItemTypeSeries, Title: "Bootstrap Show",
		Status: string(MatchOutcomeMatched), TmdbID: tmdbID,
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
	h.service.providerIDRepo = newFakeProviderIDRepo()
	h.service.staleIDRepo = newFakeStaleIDRepo()
	metadataProvider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: "Bootstrap Show",
		ProviderIDs: map[string]string{
			contentid.ProviderTMDB: tmdbID,
			contentid.ProviderTVDB: tvdbID,
		},
	}}
	episodeProvider := &capturingEpisodeBootstrapProvider{}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID:   itemID,
		ProviderIDs: map[string]string{contentid.ProviderTMDB: tmdbID},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{metadataProvider, episodeProvider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}
	if episodeProvider.providerIDs[contentid.ProviderTVDB] != tvdbID {
		t.Fatalf("season bootstrap ids = %#v, want tvdb=%s", episodeProvider.providerIDs, tvdbID)
	}
}

func TestFindExistingByProviderIDsUsesDurableRepository(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "existing-1",
		Type:      "movie",
		Title:     "Existing Item",
		Year:      2020,
		Status:    "matched",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set("existing-1", &models.MediaItemProviderID{
		ContentID:  "existing-1",
		ItemType:   "movie",
		Provider:   "custom",
		ProviderID: "custom-123",
	})
	h.service.providerIDRepo = providerRepo

	item, err := h.service.findExistingByProviderIDs(ctx, map[string]string{"custom": "custom-123"}, "movie", "")
	if err != nil {
		t.Fatalf("findExistingByProviderIDs: %v", err)
	}
	if item == nil || item.ContentID != "existing-1" {
		t.Fatalf("found item = %#v, want existing-1", item)
	}
}

func TestFindExistingByProviderIDsRespectsDurableProviderItemType(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	for _, item := range []*models.MediaItem{
		{
			ContentID: "movie-1",
			Type:      "movie",
			Title:     "Shared ID Movie",
			Year:      1992,
			Status:    "matched",
			Studios:   []string{},
			Networks:  []string{},
			Countries: []string{},
			Genres:    []string{},
		},
		{
			ContentID: "series-1",
			Type:      "series",
			Title:     "Shared ID Series",
			Year:      2008,
			Status:    "matched",
			Studios:   []string{},
			Networks:  []string{},
			Countries: []string{},
			Genres:    []string{},
		},
	} {
		if err := h.itemRepo.Upsert(ctx, item); err != nil {
			t.Fatalf("upsert item %s: %v", item.ContentID, err)
		}
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set("movie-1", &models.MediaItemProviderID{
		ContentID:  "movie-1",
		ItemType:   "movie",
		Provider:   "tmdb",
		ProviderID: "37264",
	})
	providerRepo.set("series-1", &models.MediaItemProviderID{
		ContentID:  "series-1",
		ItemType:   "series",
		Provider:   "tmdb",
		ProviderID: "37264",
	})
	h.service.providerIDRepo = providerRepo

	movie, err := h.service.findExistingByProviderIDs(ctx, map[string]string{"tmdb": "37264"}, "movie", "")
	if err != nil {
		t.Fatalf("findExistingByProviderIDs movie: %v", err)
	}
	if movie == nil || movie.ContentID != "movie-1" {
		t.Fatalf("movie match = %#v, want movie-1", movie)
	}

	series, err := h.service.findExistingByProviderIDs(ctx, map[string]string{"tmdb": "37264"}, "series", "")
	if err != nil {
		t.Fatalf("findExistingByProviderIDs series: %v", err)
	}
	if series == nil || series.ContentID != "series-1" {
		t.Fatalf("series match = %#v, want series-1", series)
	}
}

func TestProcess_LoadsAndPersistsDurableProviderIDs(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "existing-1",
		Type:      "movie",
		Title:     "Old Title",
		Status:    "matched",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set("existing-1", &models.MediaItemProviderID{
		ContentID:  "existing-1",
		ItemType:   "movie",
		Provider:   "custom",
		ProviderID: "custom-123",
	})
	h.service.providerIDRepo = providerRepo

	provider := &capturingMetadataProvider{
		response: &MetadataResult{
			HasMetadata: true,
			Title:       "Updated Title",
			ProviderIDs: map[string]string{
				"custom":    "custom-123",
				"metadb":    "existing-1",
				"_filepath": "/media/existing-1.mkv",
				"oshash":    "deadbeef",
			},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "existing-1",
		Language:  "en",
		Mode:      ModeManualRefresh,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	req := provider.lastRequest()
	if got := req.ProviderIDs["custom"]; got != "custom-123" {
		t.Fatalf("provider request custom id = %q, want custom-123", got)
	}

	providerRepo.mu.Lock()
	replace := providerRepo.lastReplace["existing-1"]
	providerRepo.mu.Unlock()
	if got := replace["custom"]; got != "custom-123" {
		t.Fatalf("persisted custom id = %q, want custom-123", got)
	}
	if _, ok := replace["metadb"]; ok {
		t.Fatal("persisted metadb id unexpectedly")
	}
	if _, ok := replace["_filepath"]; ok {
		t.Fatal("persisted _filepath unexpectedly")
	}
	if _, ok := replace["oshash"]; ok {
		t.Fatal("persisted oshash unexpectedly")
	}

	item, err := h.itemRepo.GetByID(ctx, "existing-1")
	if err != nil {
		t.Fatalf("get updated item: %v", err)
	}
	if item.Title != "Updated Title" {
		t.Fatalf("item title = %q, want Updated Title", item.Title)
	}
}

func TestProcess_InitialMatchSuppressesRecordedStaleProviderIDs(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "existing-1",
		Type:      "movie",
		Title:     "Old Title",
		Year:      2001,
		Status:    "pending",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	staleRepo := newFakeStaleIDRepo()
	staleRepo.set("existing-1", &models.StaleMediaID{
		ContentID:  "existing-1",
		Provider:   "tmdb",
		ProviderID: "dead-tmdb-id",
	})
	h.service.staleIDRepo = staleRepo

	provider := &capturingMetadataProvider{
		response: &MetadataResult{
			HasMetadata: true,
			Title:       "Recovered Title",
			ProviderIDs: map[string]string{"metadb": "existing-1"},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "existing-1",
		Language:  "en",
		Mode:      ModeInitialMatch,
		Hints: &MatchHints{
			Title:  "Old Title",
			Year:   2001,
			Type:   "movie",
			TmdbID: "dead-tmdb-id",
		},
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	req := provider.lastRequest()
	if got := req.ProviderIDs["tmdb"]; got != "" {
		t.Fatalf("provider request tmdb id = %q, want empty", got)
	}
}

func TestMergeAndPersist_DoesNotReusePendingProviderMatch(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-existing",
		Type:      "series",
		Title:     "Example Show",
		Year:      2024,
		Status:    "pending",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set("pending-existing", &models.MediaItemProviderID{
		ContentID:  "pending-existing",
		ItemType:   "series",
		Provider:   "custom",
		ProviderID: "series-123",
	})
	h.service.providerIDRepo = providerRepo

	result, err := h.service.mergeAndPersist(ctx, ProcessRequest{
		Mode: ModeInitialMatch,
	}, &MetadataResult{
		HasMetadata: true,
		Title:       "Example Show",
		Year:        2024,
		ProviderIDs: map[string]string{"custom": "series-123"},
	}, nil, nil, nil, "series")
	if err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	if result == nil || result.ContentID == "" {
		t.Fatalf("result = %#v, want non-empty content id", result)
	}
	if result.ContentID == "pending-existing" {
		t.Fatal("expected pending provider-id match to be ignored")
	}
}

func TestMergeAndPersist_DoesNotRebindSkeletonToPendingProviderMatch(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-source",
		Type:      "series",
		Title:     "Example Show Alt Root",
		Year:      2024,
		Status:    "pending",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert source item: %v", err)
	}
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-target",
		Type:      "series",
		Title:     "Example Show",
		Year:      2024,
		Status:    "pending",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert target item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set("pending-target", &models.MediaItemProviderID{
		ContentID:  "pending-target",
		ItemType:   "series",
		Provider:   "custom",
		ProviderID: "series-123",
	})
	h.service.providerIDRepo = providerRepo

	result, err := h.service.mergeAndPersist(ctx, ProcessRequest{
		ContentID: "pending-source",
		Mode:      ModeInitialMatch,
	}, &MetadataResult{
		HasMetadata: true,
		Title:       "Example Show",
		Year:        2024,
		ProviderIDs: map[string]string{"custom": "series-123"},
	}, nil, nil, nil, "series")
	if err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	if result == nil || result.ContentID != "pending-source" {
		t.Fatalf("result = %#v, want content_id pending-source", result)
	}
}

func TestIsProvisionalOwnershipStatus_IncludesAmbiguous(t *testing.T) {
	if !isProvisionalOwnershipStatus("ambiguous") {
		t.Fatal("expected ambiguous items to be treated as provisional ownership")
	}
}

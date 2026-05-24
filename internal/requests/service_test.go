package requests

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func TestCreateRequestQuotaExceeded(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalMaxRequests = 1
	store.count = 1
	service := newTestService(store)

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err == nil {
		t.Fatal("expected quota error")
	}
	var quota QuotaError
	if !errors.As(err, &quota) {
		t.Fatalf("error = %v, want QuotaError", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created requests = %d, want 0", len(store.created))
	}
}

func TestCreateRequestActiveDuplicateBlocks(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.count = 100
	store.active[MediaTypeMovie][550] = &Request{
		ID:        "req-existing",
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Status:    StatusQueued,
		Outcome:   OutcomeActive,
	}
	service := newTestService(store)

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if !errors.Is(err, ErrAlreadyRequested) {
		t.Fatalf("error = %v, want ErrAlreadyRequested", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created requests = %d, want 0", len(store.created))
	}
}

func TestCreateRequestAutoApprovalRequiresConfiguredIntegration(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	service := newTestService(store)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusPending {
		t.Fatalf("status = %q, want pending", req.Status)
	}
}

func TestCreateRequestAutoApprovesWithConfiguredIntegration(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	qualityProfileID := 1
	store.integrations = []Integration{{
		Kind:             "radarr",
		Enabled:          true,
		BaseURL:          "http://radarr.local",
		APIKeyRef:        "request.radarr.api_key",
		RootFolder:       "/movies",
		QualityProfileID: &qualityProfileID,
	}}
	service := newTestService(store)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusApproved {
		t.Fatalf("status = %q, want approved", req.Status)
	}
}

func TestCreateRequestAutoApprovalSubmitsMovie(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	qualityProfileID := 1
	store.integrations = []Integration{{
		Kind:             "radarr",
		Enabled:          true,
		BaseURL:          "http://radarr.local",
		APIKeyRef:        "requests.radarr.api_key",
		RootFolder:       "/movies",
		QualityProfileID: &qualityProfileID,
	}}
	adapter := &fakeMovieAdapter{result: FulfillmentResult{
		IntegrationKind: "radarr",
		ExternalID:      "123",
		ExternalStatus:  "queued",
	}}
	service := newTestService(store)
	service.SetSecretResolver(fakeSecrets{"requests.radarr.api_key": "radarr-key"})
	service.SetFulfillmentAdapters(adapter, nil)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusQueued || req.IntegrationKind != "radarr" || req.ExternalID != "123" {
		t.Fatalf("request = %+v, want queued radarr external id", req)
	}
	if adapter.calls != 1 {
		t.Fatalf("adapter calls = %d, want 1", adapter.calls)
	}
	if got := adapter.gotIntegration.APIKeyRef; got != "radarr-key" {
		t.Fatalf("adapter api key = %q, want resolved key", got)
	}
}

func TestCreateRequestSubmissionFailureMarksFailed(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	qualityProfileID := 1
	store.integrations = []Integration{{
		Kind:             "radarr",
		Enabled:          true,
		BaseURL:          "http://radarr.local",
		APIKeyRef:        "radarr-key",
		RootFolder:       "/movies",
		QualityProfileID: &qualityProfileID,
	}}
	adapter := &fakeMovieAdapter{err: errors.New("radarr unavailable")}
	service := newTestService(store)
	service.SetFulfillmentAdapters(adapter, nil)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Outcome != OutcomeFailed || req.LastError != "radarr unavailable" {
		t.Fatalf("request = %+v, want failed outcome with adapter error", req)
	}
}

func TestCreateRequestEnrichesSeriesTVDBID(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	tmdbClient := &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{TVDBID: 12345}}
	service := newTestServiceWithTMDB(store, tmdbClient)

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries,
		TMDBID:    1399,
		Title:     "Game of Thrones",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created requests = %d, want 1", len(store.created))
	}
	if store.created[0].Input.TVDBID == nil || *store.created[0].Input.TVDBID != 12345 {
		t.Fatalf("tvdb_id = %v, want 12345", store.created[0].Input.TVDBID)
	}
}

func TestCreateRequestNoActiveDuplicateCreatesRequest(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	service := newTestService(store)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusPending {
		t.Fatalf("status = %q, want pending", req.Status)
	}
	if len(store.created) != 1 {
		t.Fatalf("created requests = %d, want 1", len(store.created))
	}
}

func TestSearchEnrichmentHidesOtherRequesterID(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.active[MediaTypeMovie][550] = &Request{
		ID:                "req-existing",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Status:            StatusQueued,
		Outcome:           OutcomeActive,
		RequestedByUserID: 2,
	}
	tmdbClient := &fakeTMDBClient{page: &tmdb.MediaPage{
		Page:         1,
		TotalPages:   1,
		TotalResults: 1,
		Results: []tmdb.MediaResult{{
			ID:        550,
			MediaType: "movie",
			Title:     "Fight Club",
			Year:      1999,
		}},
	}}
	service := newTestServiceWithTMDB(store, tmdbClient)

	result, err := service.Search(context.Background(), testViewer(1), "fight", MediaTypeMovie, 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(result.Results))
	}
	state := result.Results[0].Request
	if state.Status != StatusQueued || state.Requestable {
		t.Fatalf("state = %+v, want queued non-requestable", state)
	}
	if state.RequestID != "" {
		t.Fatalf("request id leaked as %q", state.RequestID)
	}
}

func TestSearchEnrichmentShowsOwnRequestID(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.active[MediaTypeMovie][550] = &Request{
		ID:                "req-existing",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Status:            StatusQueued,
		Outcome:           OutcomeActive,
		RequestedByUserID: 2,
	}
	tmdbClient := &fakeTMDBClient{page: &tmdb.MediaPage{
		Page:         1,
		TotalPages:   1,
		TotalResults: 1,
		Results: []tmdb.MediaResult{{
			ID:        550,
			MediaType: "movie",
			Title:     "Fight Club",
		}},
	}}
	service := newTestServiceWithTMDB(store, tmdbClient)

	result, err := service.Search(context.Background(), testViewer(2), "fight", MediaTypeMovie, 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got := result.Results[0].Request.RequestID; got != "req-existing" {
		t.Fatalf("request id = %q, want req-existing", got)
	}
}

func TestReconcileRequestsCompletesFromCatalogPresence(t *testing.T) {
	store := newFakeStore()
	store.candidates = []*Request{{
		ID:        "req-1",
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Status:    StatusQueued,
		Outcome:   OutcomeActive,
	}}
	service := NewService(store, &fakeTMDBClient{}, &fakePresence{available: map[MediaType]map[int]bool{
		MediaTypeMovie: {550: true},
	}})

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Completed != 1 || len(store.statusUpdates) != 1 || store.statusUpdates[0] != StatusCompleted {
		t.Fatalf("result = %+v statusUpdates = %+v, want one completed update", result, store.statusUpdates)
	}
}

func TestReconcileRequestsMarksDownloadingFromAdapter(t *testing.T) {
	store := newFakeStore()
	qualityProfileID := 1
	store.integrations = []Integration{{
		Kind:             "radarr",
		Enabled:          true,
		BaseURL:          "http://radarr.local",
		APIKeyRef:        "radarr-key",
		RootFolder:       "/movies",
		QualityProfileID: &qualityProfileID,
	}}
	store.candidates = []*Request{{
		ID:         "req-1",
		MediaType:  MediaTypeMovie,
		TMDBID:     550,
		Status:     StatusQueued,
		Outcome:    OutcomeActive,
		ExternalID: "123",
	}}
	adapter := &fakeMovieAdapter{status: FulfillmentStatus{
		Status:          StatusDownloading,
		IntegrationKind: "radarr",
		ExternalID:      "123",
		ExternalStatus:  "downloading",
	}}
	service := newTestService(store)
	service.SetFulfillmentAdapters(adapter, nil)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Downloading != 1 || len(store.statusUpdates) != 1 || store.statusUpdates[0] != StatusDownloading {
		t.Fatalf("result = %+v statusUpdates = %+v, want one downloading update", result, store.statusUpdates)
	}
	if adapter.statusCalls != 1 {
		t.Fatalf("status adapter calls = %d, want 1", adapter.statusCalls)
	}
}

func newTestService(store *fakeStore) *Service {
	return newTestServiceWithTMDB(store, &fakeTMDBClient{})
}

func newTestServiceWithTMDB(store *fakeStore, tmdbClient *fakeTMDBClient) *Service {
	service := NewService(store, tmdbClient, &fakePresence{})
	service.Now = func() time.Time { return time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC) }
	return service
}

func testViewer(userID int) Viewer {
	return Viewer{UserID: userID, ProfileID: "profile-1"}
}

type fakeStore struct {
	settings      Settings
	limit         *UserLimit
	count         int
	active        map[MediaType]map[int]*Request
	created       []CreateRequestRecord
	integrations  []Integration
	queued        []QueueUpdate
	candidates    []*Request
	statusUpdates []Status
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings: Settings{
			GlobalMaxRequests: 5,
			GlobalWindowDays:  7,
		},
		active: map[MediaType]map[int]*Request{
			MediaTypeMovie:  {},
			MediaTypeSeries: {},
		},
	}
}

func (f *fakeStore) GetSettings(context.Context) (Settings, error) {
	return f.settings, nil
}

func (f *fakeStore) UpdateSettings(_ context.Context, settings Settings) (Settings, error) {
	f.settings = settings
	return settings, nil
}

func (f *fakeStore) GetUserLimit(context.Context, int) (*UserLimit, error) {
	return f.limit, nil
}

func (f *fakeStore) UpsertUserLimit(_ context.Context, limit UserLimit) (*UserLimit, error) {
	f.limit = &limit
	return &limit, nil
}

func (f *fakeStore) CountUserRequestsSince(context.Context, int, time.Time) (int, error) {
	return f.count, nil
}

func (f *fakeStore) ListActiveByTMDB(_ context.Context, mediaType MediaType, ids []int) (map[int]*Request, error) {
	out := map[int]*Request{}
	for _, id := range ids {
		if req := f.active[mediaType][id]; req != nil {
			out[id] = req
		}
	}
	return out, nil
}

func (f *fakeStore) CreateRequest(_ context.Context, input CreateRequestRecord) (*Request, error) {
	f.created = append(f.created, input)
	return &Request{
		ID:                   input.ID,
		Provider:             "tmdb",
		MediaType:            input.Input.MediaType,
		TMDBID:               input.Input.TMDBID,
		TVDBID:               input.Input.TVDBID,
		IMDbID:               input.Input.IMDbID,
		Title:                input.Input.Title,
		Status:               input.Status,
		Outcome:              input.Outcome,
		RequestedByUserID:    input.Requester.UserID,
		RequestedByProfileID: input.Requester.ProfileID,
		CreatedAt:            input.Now,
		UpdatedAt:            input.Now,
	}, nil
}

func (f *fakeStore) GetRequest(context.Context, string) (*Request, error) {
	return nil, ErrNotFound
}

func (f *fakeStore) ListReconciliationCandidates(context.Context, int) ([]*Request, error) {
	return f.candidates, nil
}

func (f *fakeStore) ListMine(context.Context, int, ListFilter) ([]*Request, error) {
	return nil, nil
}

func (f *fakeStore) ListAdmin(context.Context, ListFilter) ([]*Request, error) {
	return nil, nil
}

func (f *fakeStore) SetStatus(_ context.Context, id string, status Status, _ Viewer) (*Request, error) {
	f.statusUpdates = append(f.statusUpdates, status)
	return &Request{
		ID:      id,
		Status:  status,
		Outcome: OutcomeActive,
	}, nil
}

func (f *fakeStore) MarkQueued(_ context.Context, id string, update QueueUpdate, _ Viewer) (*Request, error) {
	f.queued = append(f.queued, update)
	return &Request{
		ID:              id,
		Status:          StatusQueued,
		Outcome:         OutcomeActive,
		IntegrationKind: update.IntegrationKind,
		ExternalID:      update.ExternalID,
		ExternalStatus:  update.ExternalStatus,
	}, nil
}

func (f *fakeStore) SetOutcome(_ context.Context, id string, outcome Outcome, _ Viewer, message string) (*Request, error) {
	return &Request{
		ID:        id,
		Outcome:   outcome,
		LastError: message,
	}, nil
}

func (f *fakeStore) ListIntegrations(context.Context) ([]Integration, error) {
	return f.integrations, nil
}

func (f *fakeStore) UpsertIntegration(context.Context, Integration) (*Integration, error) {
	return nil, nil
}

func TestListStudiosReturnsBundleWithDuotoneLogos(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})

	studios, err := service.ListStudios(context.Background(), testViewer(1))
	if err != nil {
		t.Fatalf("ListStudios: %v", err)
	}
	if len(studios) != len(BundledStudios) {
		t.Fatalf("len = %d, want %d", len(studios), len(BundledStudios))
	}

	for _, s := range studios {
		if s.LogoURL == nil || *s.LogoURL == "" {
			t.Errorf("studio %q missing logo URL", s.Slug)
			continue
		}
		if !strings.Contains(*s.LogoURL, "filter(duotone,ffffff,bababa)") {
			t.Errorf("studio %q logo URL missing duotone filter: %s", s.Slug, *s.LogoURL)
		}
	}
}

func TestListNetworksReturnsBundleWithDuotoneLogos(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})

	networks, err := service.ListNetworks(context.Background(), testViewer(1))
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	if len(networks) != len(BundledNetworks) {
		t.Fatalf("len = %d, want %d", len(networks), len(BundledNetworks))
	}
	for _, n := range networks {
		if n.LogoURL == nil || *n.LogoURL == "" {
			t.Errorf("network %q missing logo URL", n.Slug)
			continue
		}
		if !strings.Contains(*n.LogoURL, "filter(duotone,ffffff,bababa)") {
			t.Errorf("network %q logo URL missing duotone filter: %s", n.Slug, *n.LogoURL)
		}
	}
}

func TestListGenresReturnsBundleWithSeriesSupportFlag(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})

	genres, err := service.ListGenres(context.Background(), testViewer(1))
	if err != nil {
		t.Fatalf("ListGenres: %v", err)
	}
	if len(genres) != len(BundledGenres) {
		t.Fatalf("len = %d, want %d", len(genres), len(BundledGenres))
	}
	for _, g := range genres {
		switch g.Slug {
		case "action", "comedy", "drama", "sci-fi", "animation", "documentary":
			if !g.SeriesSupported {
				t.Errorf("%s should support series", g.Slug)
			}
		case "horror", "romance":
			if g.SeriesSupported {
				t.Errorf("%s should not support series", g.Slug)
			}
		}
		if g.GradientFrom == "" || g.GradientTo == "" {
			t.Errorf("%s missing gradient", g.Slug)
		}
		if g.LogoURL != nil {
			t.Errorf("%s should not have a logo URL", g.Slug)
		}
	}
}

func TestBrowseStudioReturnsEnrichedMovies(t *testing.T) {
	tmdbClient := &fakeTMDBClient{discoverPage: &tmdb.MediaPage{
		Page:         1,
		TotalPages:   2,
		TotalResults: 20,
		Results: []tmdb.MediaResult{
			{ID: 24428, MediaType: "movie", Title: "The Avengers", Year: 2012, Popularity: 100.5},
		},
	}}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	resp, err := service.BrowseStudio(context.Background(), testViewer(1), "marvel-studios", "popularity", 1)
	if err != nil {
		t.Fatalf("BrowseStudio: %v", err)
	}
	if resp.Kind != "studio" || resp.Slug != "marvel-studios" || resp.MediaType != MediaTypeMovie {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Page != 1 || resp.TotalPages != 2 {
		t.Errorf("pagination = %d/%d", resp.Page, resp.TotalPages)
	}
	if len(resp.Results) != 1 || resp.Results[0].TMDBID != 24428 {
		t.Errorf("results = %+v", resp.Results)
	}
	if resp.Results[0].Availability == "" {
		t.Error("availability should be enriched")
	}
}

func TestBrowseStudioUnknownSlugReturnsNotFound(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})
	_, err := service.BrowseStudio(context.Background(), testViewer(1), "not-a-studio", "popularity", 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestBrowseStudioRejectsBadSort(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})
	_, err := service.BrowseStudio(context.Background(), testViewer(1), "marvel-studios", "made-up-sort", 1)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestBrowseStudioDefaultsBlankSortToPopularity(t *testing.T) {
	tmdbClient := &fakeTMDBClient{discoverPage: &tmdb.MediaPage{Results: []tmdb.MediaResult{}}}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	resp, err := service.BrowseStudio(context.Background(), testViewer(1), "marvel-studios", "", 1)
	if err != nil {
		t.Fatalf("BrowseStudio: %v", err)
	}
	if resp.Sort != "popularity" {
		t.Errorf("sort = %q, want popularity (default)", resp.Sort)
	}
}

func TestBrowseNetworkReturnsSeries(t *testing.T) {
	tmdbClient := &fakeTMDBClient{discoverPage: &tmdb.MediaPage{
		Page: 1, TotalPages: 1, TotalResults: 1,
		Results: []tmdb.MediaResult{
			{ID: 1399, MediaType: "series", Title: "Game of Thrones", Year: 2011},
		},
	}}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	resp, err := service.BrowseNetwork(context.Background(), testViewer(1), "netflix", "popularity", 1)
	if err != nil {
		t.Fatalf("BrowseNetwork: %v", err)
	}
	if resp.MediaType != MediaTypeSeries {
		t.Errorf("media_type = %q, want series", resp.MediaType)
	}
}

func TestBrowseGenreRequiresMediaType(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})
	_, err := service.BrowseGenre(context.Background(), testViewer(1), "action", "", "popularity", 1)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestBrowseGenreSeriesRejectedWhenUnsupported(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})
	_, err := service.BrowseGenre(context.Background(), testViewer(1), "horror", "series", "popularity", 1)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput for horror+series", err)
	}
}

func TestBrowseGenreMovieReturnsResults(t *testing.T) {
	tmdbClient := &fakeTMDBClient{discoverPage: &tmdb.MediaPage{
		Results: []tmdb.MediaResult{{ID: 1, MediaType: "movie", Title: "Movie"}},
	}}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	resp, err := service.BrowseGenre(context.Background(), testViewer(1), "action", "movie", "popularity", 1)
	if err != nil {
		t.Fatalf("BrowseGenre: %v", err)
	}
	if resp.Kind != "genre" || resp.Slug != "action" || resp.MediaType != MediaTypeMovie {
		t.Errorf("resp = %+v", resp)
	}
}

type fakePresence struct {
	available map[MediaType]map[int]bool
}

func (f *fakePresence) LookupTMDB(_ context.Context, mediaType MediaType, ids []int) (map[int]bool, error) {
	out := map[int]bool{}
	if f.available == nil {
		return out, nil
	}
	for _, id := range ids {
		if f.available[mediaType][id] {
			out[id] = true
		}
	}
	return out, nil
}

type fakeTMDBClient struct {
	page         *tmdb.MediaPage
	externalIDs  *tmdb.ExternalIDs
	detail       *tmdb.MediaDetail
	discoverPage *tmdb.MediaPage
	discoverErr  error
}

func (f *fakeTMDBClient) SearchMedia(context.Context, string, string, int) (*tmdb.MediaPage, error) {
	return f.page, nil
}

func (f *fakeTMDBClient) DiscoverSection(context.Context, string, int) (*tmdb.MediaPage, error) {
	return f.page, nil
}

func (f *fakeTMDBClient) DiscoverPage(context.Context, string, tmdb.DiscoverParams, int) (*tmdb.MediaPage, error) {
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	if f.discoverPage != nil {
		return f.discoverPage, nil
	}
	return &tmdb.MediaPage{Results: []tmdb.MediaResult{}}, nil
}

func (f *fakeTMDBClient) GetExternalIDs(context.Context, string, int) (*tmdb.ExternalIDs, error) {
	return f.externalIDs, nil
}

func (f *fakeTMDBClient) GetMediaDetail(context.Context, string, int) (*tmdb.MediaDetail, error) {
	return f.detail, nil
}

type fakeMovieAdapter struct {
	result         FulfillmentResult
	status         FulfillmentStatus
	err            error
	statusErr      error
	calls          int
	statusCalls    int
	gotReq         Request
	gotIntegration Integration
}

func (f *fakeMovieAdapter) SubmitMovie(_ context.Context, req Request, integration Integration) (FulfillmentResult, error) {
	f.calls++
	f.gotReq = req
	f.gotIntegration = integration
	return f.result, f.err
}

func (f *fakeMovieAdapter) CheckMovieStatus(_ context.Context, req Request, integration Integration) (FulfillmentStatus, error) {
	f.statusCalls++
	f.gotReq = req
	f.gotIntegration = integration
	return f.status, f.statusErr
}

type fakeSecrets map[string]string

func (f fakeSecrets) Get(_ context.Context, key string) (string, error) {
	return f[key], nil
}

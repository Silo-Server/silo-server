package requests

import (
	"context"
	"errors"
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
	page        *tmdb.MediaPage
	externalIDs *tmdb.ExternalIDs
	detail      *tmdb.MediaDetail
}

func (f *fakeTMDBClient) SearchMedia(context.Context, string, string, int) (*tmdb.MediaPage, error) {
	return f.page, nil
}

func (f *fakeTMDBClient) DiscoverSection(context.Context, string, int) (*tmdb.MediaPage, error) {
	return f.page, nil
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

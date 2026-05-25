package requests

import (
	"context"
	"errors"
	"strings"
	"sync"
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

func TestNormalizeListFilterCapsLimit(t *testing.T) {
	cases := []struct {
		name    string
		in      ListFilter
		wantLim int
		wantOff int
	}{
		{"zero defaults", ListFilter{}, defaultRequestListLimit, 0},
		{"negative defaults", ListFilter{Limit: -10, Offset: -5}, defaultRequestListLimit, 0},
		{"under cap preserved", ListFilter{Limit: 75, Offset: 10}, 75, 10},
		{"at cap preserved", ListFilter{Limit: maxRequestListLimit, Offset: 0}, maxRequestListLimit, 0},
		{"over cap clamped", ListFilter{Limit: 1_000_000}, maxRequestListLimit, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeListFilter(tc.in)
			if got.Limit != tc.wantLim {
				t.Errorf("limit = %d, want %d", got.Limit, tc.wantLim)
			}
			if got.Offset != tc.wantOff {
				t.Errorf("offset = %d, want %d", got.Offset, tc.wantOff)
			}
		})
	}
}

func TestCreateRequestConcurrentSubmissionsRespectQuota(t *testing.T) {
	const (
		maxRequests = 5
		goroutines  = 20
	)
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalMaxRequests = maxRequests
	service := newTestService(store)

	var (
		wg         sync.WaitGroup
		successMu  sync.Mutex
		successes  int
		quotaFails int
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(tmdbID int) {
			defer wg.Done()
			_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
				MediaType: MediaTypeMovie,
				TMDBID:    tmdbID,
				Title:     "Title",
			})
			successMu.Lock()
			defer successMu.Unlock()
			if err == nil {
				successes++
				return
			}
			var quota QuotaError
			if errors.As(err, &quota) {
				quotaFails++
			}
		}(1000 + i)
	}
	wg.Wait()

	if successes != maxRequests {
		t.Fatalf("successful creations = %d, want %d", successes, maxRequests)
	}
	if successes+quotaFails != goroutines {
		t.Fatalf("non-quota errors: successes=%d quotaFails=%d total=%d", successes, quotaFails, goroutines)
	}
	if len(store.created) != maxRequests {
		t.Fatalf("stored creations = %d, want %d", len(store.created), maxRequests)
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

func TestCreateRequestAutoApprovalFallsBackToPendingOnIntegrationCheckError(t *testing.T) {
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
	service := newTestService(store)
	service.SetSecretResolver(fakeSecretError{err: errors.New("secret lookup unavailable")})

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

func TestCreateRequestBlocksWhenHydratedTVDBIDIsAvailable(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	tmdbClient := &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{TVDBID: 420105, IMDbID: "tt18076310"}}
	presence := &fakePresence{byTVDB: map[MediaType]map[int]int{
		MediaTypeSeries: {420105: 201992},
	}}
	service := NewService(store, tmdbClient, presence)

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries,
		TMDBID:    201992,
		Title:     "The Rookie: Feds",
	})
	if !errors.Is(err, ErrAlreadyAvailable) {
		t.Fatalf("err = %v, want ErrAlreadyAvailable", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created requests = %d, want 0", len(store.created))
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

func TestCreateRequestClearsPriorFailedRequest(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.requests["req-prior-failed"] = &Request{
		ID:        "req-prior-failed",
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Outcome:   OutcomeFailed,
		Status:    StatusApproved,
		LastError: "arr: decode response: json: cannot unmarshal object into Go value of type []radarr.movieResource",
	}
	store.requests["req-other-media-failed"] = &Request{
		ID:        "req-other-media-failed",
		MediaType: MediaTypeMovie,
		TMDBID:    999,
		Outcome:   OutcomeFailed,
	}
	service := newTestService(store)

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if _, ok := store.requests["req-prior-failed"]; ok {
		t.Fatal("prior failed request was not cleared")
	}
	if _, ok := store.requests["req-other-media-failed"]; !ok {
		t.Fatal("failed request for different media should not be cleared")
	}
}

func TestSearchMarksSeriesAvailableByHydratedTVDBID(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	tmdbClient := &fakeTMDBClient{
		page: &tmdb.MediaPage{
			Page: 1,
			Results: []tmdb.MediaResult{{
				ID:        201992,
				MediaType: "series",
				Title:     "The Rookie: Feds",
				Year:      2022,
			}},
		},
		externalIDsByID: map[int]*tmdb.ExternalIDs{
			201992: {TVDBID: 420105, IMDbID: "tt18076310"},
		},
	}
	presence := &fakePresence{byTVDB: map[MediaType]map[int]int{
		MediaTypeSeries: {420105: 201992},
	}}
	service := NewService(store, tmdbClient, presence)

	page, err := service.Search(context.Background(), testViewer(1), "rookie feds", MediaTypeSeries, 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got := page.Results[0].Availability; got != AvailabilityAvailable {
		t.Fatalf("availability = %q, want available", got)
	}
	if page.Results[0].Request.Reason != "already_available" {
		t.Fatalf("request reason = %q, want already_available", page.Results[0].Request.Reason)
	}
	if len(presence.got) != 1 || presence.got[0].TVDBID == nil || *presence.got[0].TVDBID != 420105 {
		t.Fatalf("presence candidates = %+v, want hydrated tvdb id", presence.got)
	}
}

func TestSearchWithNilPresenceDoesNotHydrateExternalIDs(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	tmdbClient := &fakeTMDBClient{page: &tmdb.MediaPage{Results: []tmdb.MediaResult{{
		ID:        201992,
		MediaType: "series",
		Title:     "The Rookie: Feds",
	}}}}
	service := NewService(store, tmdbClient, nil)

	_, err := service.Search(context.Background(), testViewer(1), "rookie feds", MediaTypeSeries, 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(tmdbClient.externalIDCalls) != 0 {
		t.Fatalf("external ID calls = %v, want none", tmdbClient.externalIDCalls)
	}
}

func TestSearchHydratesMultipleResultsBeforePresenceLookup(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	tmdbClient := &fakeTMDBClient{
		page: &tmdb.MediaPage{Results: []tmdb.MediaResult{
			{ID: 201992, MediaType: "series", Title: "The Rookie: Feds"},
			{ID: 1399, MediaType: "series", Title: "Game of Thrones"},
		}},
		externalIDsByID: map[int]*tmdb.ExternalIDs{
			201992: {TVDBID: 420105, IMDbID: "tt18076310"},
			1399:   {TVDBID: 121361, IMDbID: "tt0944947"},
		},
	}
	presence := &fakePresence{}
	service := NewService(store, tmdbClient, presence)

	_, err := service.Search(context.Background(), testViewer(1), "series", MediaTypeSeries, 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(presence.got) != 2 {
		t.Fatalf("presence candidates = %d, want 2", len(presence.got))
	}
	got := map[int]int{}
	for _, candidate := range presence.got {
		if candidate.TVDBID != nil {
			got[candidate.TMDBID] = *candidate.TVDBID
		}
	}
	if got[201992] != 420105 || got[1399] != 121361 {
		t.Fatalf("hydrated tvdb ids = %+v", got)
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

func TestSearchWithoutMediaTypeSearchesMoviesAndSeries(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.active[MediaTypeSeries][1399] = &Request{
		ID:                "req-series",
		MediaType:         MediaTypeSeries,
		TMDBID:            1399,
		Status:            StatusQueued,
		Outcome:           OutcomeActive,
		RequestedByUserID: 1,
	}
	tmdbClient := &fakeTMDBClient{page: &tmdb.MediaPage{
		Page:         1,
		TotalPages:   1,
		TotalResults: 2,
		Results: []tmdb.MediaResult{
			{
				ID:        550,
				MediaType: "movie",
				Title:     "Fight Club",
			},
			{
				ID:        1399,
				MediaType: "series",
				Title:     "Fight Club: The Series",
			},
		},
	}}
	service := newTestServiceWithTMDB(store, tmdbClient)

	result, err := service.Search(context.Background(), testViewer(1), "fight", "", 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if tmdbClient.searchMediaType != "all" {
		t.Fatalf("search media type = %q, want all", tmdbClient.searchMediaType)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(result.Results))
	}
	if result.Results[0].MediaType != MediaTypeMovie {
		t.Fatalf("results[0].MediaType = %q, want movie", result.Results[0].MediaType)
	}
	if result.Results[1].MediaType != MediaTypeSeries {
		t.Fatalf("results[1].MediaType = %q, want series", result.Results[1].MediaType)
	}
	if result.Results[1].Request.RequestID != "req-series" {
		t.Fatalf("series request id = %q, want req-series", result.Results[1].Request.RequestID)
	}
}

func TestDisabledRequestsBlockUserSurfaces(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = false
	store.requests["req-1"] = &Request{
		ID:                "req-1",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Status:            StatusPending,
		Outcome:           OutcomeActive,
		RequestedByUserID: 1,
	}
	tmdbClient := &fakeTMDBClient{
		page:         &tmdb.MediaPage{Results: []tmdb.MediaResult{{ID: 550, MediaType: "movie", Title: "Fight Club"}}},
		detail:       &tmdb.MediaDetail{ID: 550, MediaType: "movie", Title: "Fight Club"},
		discoverPage: &tmdb.MediaPage{Results: []tmdb.MediaResult{{ID: 550, MediaType: "movie", Title: "Fight Club"}}},
	}
	service := newTestServiceWithTMDB(store, tmdbClient)
	viewer := testViewer(1)

	cases := []struct {
		name string
		call func() error
	}{
		{"search", func() error {
			_, err := service.Search(context.Background(), viewer, "fight", MediaTypeMovie, 1)
			return err
		}},
		{"discover all", func() error {
			_, err := service.DiscoverAll(context.Background(), viewer)
			return err
		}},
		{"discover section", func() error {
			_, err := service.Discover(context.Background(), viewer, "popular_movies", 1)
			return err
		}},
		{"detail", func() error {
			_, err := service.GetDetail(context.Background(), viewer, MediaTypeMovie, 550)
			return err
		}},
		{"create", func() error {
			_, err := service.CreateRequest(context.Background(), viewer, CreateRequestInput{
				MediaType: MediaTypeMovie,
				TMDBID:    550,
				Title:     "Fight Club",
			})
			return err
		}},
		{"mine", func() error {
			_, err := service.ListMine(context.Background(), viewer, ListFilter{})
			return err
		}},
		{"get", func() error {
			_, err := service.GetRequest(context.Background(), viewer, "req-1")
			return err
		}},
		{"cancel", func() error {
			_, err := service.Cancel(context.Background(), viewer, "req-1", "")
			return err
		}},
		{"studios", func() error {
			_, err := service.ListStudios(context.Background(), viewer)
			return err
		}},
		{"networks", func() error {
			_, err := service.ListNetworks(context.Background(), viewer)
			return err
		}},
		{"genres", func() error {
			_, err := service.ListGenres(context.Background(), viewer)
			return err
		}},
		{"browse studio", func() error {
			_, err := service.BrowseStudio(context.Background(), viewer, "marvel-studios", "popularity", 1)
			return err
		}},
		{"browse network", func() error {
			_, err := service.BrowseNetwork(context.Background(), viewer, "netflix", "popularity", 1)
			return err
		}},
		{"browse genre", func() error {
			_, err := service.BrowseGenre(context.Background(), viewer, "action", MediaTypeMovie, "popularity", 1)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, ErrRequestsDisabled) {
				t.Fatalf("err = %v, want ErrRequestsDisabled", err)
			}
		})
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

func TestReconcileRequestsCompletesByStoredTVDBID(t *testing.T) {
	store := newFakeStore()
	tvdbID := 420105
	store.candidates = []*Request{{
		ID:        "req-1",
		MediaType: MediaTypeSeries,
		TMDBID:    201992,
		TVDBID:    &tvdbID,
		Status:    StatusQueued,
		Outcome:   OutcomeActive,
	}}
	presence := &fakePresence{byTVDB: map[MediaType]map[int]int{
		MediaTypeSeries: {420105: 201992},
	}}
	service := NewService(store, &fakeTMDBClient{}, presence)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("completed = %d, want 1", result.Completed)
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

func TestCancelOwnerCanWithdrawPendingRequest(t *testing.T) {
	store := newFakeStore()
	store.requests["req-mine"] = &Request{
		ID:                "req-mine",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Status:            StatusPending,
		Outcome:           OutcomeActive,
		RequestedByUserID: 7,
	}
	service := newTestService(store)

	req, err := service.Cancel(context.Background(), Viewer{UserID: 7, ProfileID: "profile-1"}, "req-mine", "no longer want")
	if err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if req.Outcome != OutcomeCancelled {
		t.Fatalf("Outcome = %q, want cancelled", req.Outcome)
	}
}

func TestCancelNonOwnerForbidden(t *testing.T) {
	store := newFakeStore()
	store.requests["req-someone-else"] = &Request{
		ID:                "req-someone-else",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Status:            StatusPending,
		Outcome:           OutcomeActive,
		RequestedByUserID: 7,
	}
	service := newTestService(store)

	_, err := service.Cancel(context.Background(), Viewer{UserID: 8, ProfileID: "profile-2"}, "req-someone-else", "")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestCancelAdminCanCancelAnyPending(t *testing.T) {
	store := newFakeStore()
	store.requests["req-other"] = &Request{
		ID:                "req-other",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Status:            StatusPending,
		Outcome:           OutcomeActive,
		RequestedByUserID: 7,
	}
	service := newTestService(store)

	_, err := service.Cancel(context.Background(), Viewer{UserID: 99, IsAdmin: true}, "req-other", "house cleaning")
	if err != nil {
		t.Fatalf("admin Cancel returned error: %v", err)
	}
}

func TestCancelRejectsRequestsAlreadyInFulfillment(t *testing.T) {
	cases := []struct {
		name string
		req  Request
	}{
		{"approved", Request{Status: StatusApproved, Outcome: OutcomeActive}},
		{"queued", Request{Status: StatusQueued, Outcome: OutcomeActive, IntegrationKind: "radarr", ExternalID: "42"}},
		{"downloading", Request{Status: StatusDownloading, Outcome: OutcomeActive, IntegrationKind: "radarr", ExternalID: "42"}},
		{"completed", Request{Status: StatusCompleted, Outcome: OutcomeActive}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			tc.req.ID = "req-x"
			tc.req.RequestedByUserID = 7
			store.requests["req-x"] = &tc.req
			service := newTestService(store)

			_, err := service.Cancel(context.Background(), Viewer{UserID: 7, ProfileID: "profile-1"}, "req-x", "")
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("err = %v, want ErrInvalidState for %s", err, tc.name)
			}
		})
	}
}

func TestDeclineRejectsApprovedRequests(t *testing.T) {
	store := newFakeStore()
	store.requests["req-approved"] = &Request{
		ID:        "req-approved",
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Status:    StatusApproved,
		Outcome:   OutcomeActive,
	}
	service := newTestService(store)

	_, err := service.Decline(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-approved", "changed mind")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState (approved is owned by the reconciler)", err)
	}
}

func TestDeclineRejectsQueuedRequests(t *testing.T) {
	store := newFakeStore()
	store.requests["req-1"] = &Request{
		ID:              "req-1",
		MediaType:       MediaTypeMovie,
		TMDBID:          550,
		Status:          StatusQueued,
		Outcome:         OutcomeActive,
		IntegrationKind: "radarr",
		ExternalID:      "42",
	}
	service := newTestService(store)

	_, err := service.Decline(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1", "not needed")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState", err)
	}
}

func TestRetryResubmitsFailedQueuedRequest(t *testing.T) {
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
	store.requests["req-1"] = &Request{
		ID:        "req-1",
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Status:    StatusQueued,
		Outcome:   OutcomeFailed,
	}
	adapter := &fakeMovieAdapter{result: FulfillmentResult{
		IntegrationKind: "radarr",
		ExternalID:      "99",
		ExternalStatus:  "queued",
	}}
	service := newTestService(store)
	service.SetFulfillmentAdapters(adapter, nil)

	req, err := service.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1")
	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("adapter calls = %d, want 1", adapter.calls)
	}
	if adapter.gotReq.Status != StatusApproved {
		t.Fatalf("submitted status = %q, want approved", adapter.gotReq.Status)
	}
	if req.Status != StatusQueued || req.ExternalID != "99" {
		t.Fatalf("request = %+v, want re-queued with external id 99", req)
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
	mu            sync.Mutex
	settings      Settings
	limit         *UserLimit
	count         int
	active        map[MediaType]map[int]*Request
	created       []CreateRequestRecord
	integrations  []Integration
	queued        []QueueUpdate
	candidates    []*Request
	statusUpdates []Status
	requests      map[string]*Request
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings: Settings{
			RequestsEnabled:   true,
			GlobalMaxRequests: 5,
			GlobalWindowDays:  7,
		},
		active: map[MediaType]map[int]*Request{
			MediaTypeMovie:  {},
			MediaTypeSeries: {},
		},
		requests: map[string]*Request{},
	}
}

func (f *fakeStore) GetSettings(context.Context) (Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, nil
}

func (f *fakeStore) UpdateSettings(_ context.Context, settings Settings) (Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = settings
	return settings, nil
}

func (f *fakeStore) GetUserLimit(context.Context, int) (*UserLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.limit, nil
}

func (f *fakeStore) UpsertUserLimit(_ context.Context, limit UserLimit) (*UserLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limit = &limit
	return &limit, nil
}

func (f *fakeStore) CountUserRequestsSince(_ context.Context, userID int, since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	used := f.count
	for _, prior := range f.created {
		if prior.Requester.UserID != userID {
			continue
		}
		if prior.Now.Before(since) {
			continue
		}
		used++
	}
	return used, nil
}

func (f *fakeStore) ListActiveByTMDB(_ context.Context, mediaType MediaType, ids []int) (map[int]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]*Request{}
	for _, id := range ids {
		if req := f.active[mediaType][id]; req != nil {
			out[id] = req
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteFailedByTMDB(_ context.Context, mediaType MediaType, tmdbID int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	deleted := 0
	for id, req := range f.requests {
		if req.MediaType == mediaType && req.TMDBID == tmdbID && req.Outcome == OutcomeFailed {
			delete(f.requests, id)
			deleted++
		}
	}
	return deleted, nil
}

func (f *fakeStore) CreateRequest(_ context.Context, input CreateRequestRecord) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if input.Quota != nil {
		used := f.count
		for _, prior := range f.created {
			if prior.Requester.UserID != input.Quota.UserID {
				continue
			}
			if prior.Now.Before(input.Quota.WindowStart) {
				continue
			}
			used++
		}
		if used >= input.Quota.MaxRequests {
			return nil, ErrQuotaExceeded
		}
	}
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

func (f *fakeStore) GetRequest(_ context.Context, id string) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.requests[strings.TrimSpace(id)]
	if req == nil {
		return nil, ErrNotFound
	}
	copy := *req
	return &copy, nil
}

func (f *fakeStore) ListReconciliationCandidates(context.Context, int) ([]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.candidates, nil
}

func (f *fakeStore) ListMine(context.Context, int, ListFilter) ([]*Request, error) {
	return nil, nil
}

func (f *fakeStore) ListAdmin(context.Context, ListFilter) ([]*Request, error) {
	return nil, nil
}

func (f *fakeStore) SetStatus(_ context.Context, id string, status Status, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusUpdates = append(f.statusUpdates, status)
	req := f.requests[id]
	if req == nil {
		req = &Request{ID: id, Outcome: OutcomeActive}
		f.requests[id] = req
	}
	req.Status = status
	copy := *req
	return &copy, nil
}

func (f *fakeStore) MarkQueued(_ context.Context, id string, update QueueUpdate, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, update)
	req := f.requests[id]
	if req == nil {
		req = &Request{ID: id}
		f.requests[id] = req
	}
	req.Status = StatusQueued
	req.Outcome = OutcomeActive
	req.IntegrationKind = update.IntegrationKind
	req.ExternalID = update.ExternalID
	req.ExternalStatus = update.ExternalStatus
	copy := *req
	return &copy, nil
}

func (f *fakeStore) SetOutcome(_ context.Context, id string, outcome Outcome, _ Viewer, message string) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.requests[id]
	if req == nil {
		req = &Request{ID: id}
		f.requests[id] = req
	}
	req.Outcome = outcome
	req.LastError = message
	copy := *req
	return &copy, nil
}

func (f *fakeStore) ListIntegrations(context.Context) ([]Integration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.integrations, nil
}

func (f *fakeStore) UpsertIntegration(context.Context, Integration) (*Integration, error) {
	return nil, nil
}

func (f *fakeStore) UpsertIntegrations(_ context.Context, integrations []Integration) ([]Integration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.integrations = append([]Integration(nil), integrations...)
	return append([]Integration(nil), integrations...), nil
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
	mu        sync.Mutex
	available map[MediaType]map[int]bool
	byTVDB    map[MediaType]map[int]int
	got       []PresenceCandidate
}

func (f *fakePresence) Lookup(_ context.Context, mediaType MediaType, candidates []PresenceCandidate) (map[int]PresenceMatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]PresenceMatch{}
	f.got = append(f.got, candidates...)
	for _, candidate := range candidates {
		if f.available != nil && f.available[mediaType][candidate.TMDBID] {
			out[candidate.TMDBID] = PresenceMatch{Available: true, MatchedProvider: "tmdb"}
			continue
		}
		if candidate.TVDBID != nil && f.byTVDB != nil {
			if tmdbID, ok := f.byTVDB[mediaType][*candidate.TVDBID]; ok && tmdbID == candidate.TMDBID {
				out[candidate.TMDBID] = PresenceMatch{Available: true, MatchedProvider: "tvdb"}
			}
		}
	}
	return out, nil
}

func (f *fakePresence) LookupTMDB(_ context.Context, mediaType MediaType, ids []int) (map[int]bool, error) {
	out := map[int]bool{}
	for _, id := range ids {
		matches, err := f.Lookup(context.Background(), mediaType, []PresenceCandidate{{TMDBID: id}})
		if err != nil {
			return nil, err
		}
		if matches[id].Available {
			out[id] = true
		}
	}
	return out, nil
}

type fakeTMDBClient struct {
	mu              sync.Mutex
	page            *tmdb.MediaPage
	externalIDs     *tmdb.ExternalIDs
	externalIDsByID map[int]*tmdb.ExternalIDs
	externalIDCalls []int
	detail          *tmdb.MediaDetail
	discoverPage    *tmdb.MediaPage
	discoverErr     error
	searchMediaType string
}

func (f *fakeTMDBClient) SearchMedia(_ context.Context, mediaType, _ string, _ int) (*tmdb.MediaPage, error) {
	f.searchMediaType = mediaType
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

func (f *fakeTMDBClient) GetExternalIDs(_ context.Context, _ string, id int) (*tmdb.ExternalIDs, error) {
	f.mu.Lock()
	f.externalIDCalls = append(f.externalIDCalls, id)
	f.mu.Unlock()
	if f.externalIDsByID != nil {
		return f.externalIDsByID[id], nil
	}
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

type fakeSecretError struct {
	err error
}

func (f fakeSecretError) Get(context.Context, string) (string, error) {
	return "", f.err
}

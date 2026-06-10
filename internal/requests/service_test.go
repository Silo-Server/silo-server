package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
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

// TestCreateRequestAutoApprovalEmptyKeyTreatedAsUnconfigured guards that a router
// connection that is enabled + bound but has no api key (empty after the repo's
// decrypt) reads as "not configured": auto-approval is declined and the request
// stays pending, rather than being auto-approved and then failing submission when
// resolveRouterConnections skips the keyless connection. This pins the empty-key
// check in integrationConfigured against the skip in resolveRouterConnections so
// the two can't drift at the public CreateRequest surface.
func TestCreateRequestAutoApprovalEmptyKeyTreatedAsUnconfigured(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	store.integrations = []Integration{autoApproveRouterInst("router-1", "")}
	service := newTestService(store)
	router := &fakeRouterProvider{}
	service.SetRouterProvider(router)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusPending {
		t.Fatalf("status = %q, want pending (empty-key connection is unconfigured)", req.Status)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want 0 (must not submit to a keyless connection)", router.fulfillCalls)
	}
}

func TestCreateRequestAutoApprovesWithConfiguredIntegration(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	// A plugin-driven router connection that sets only the generic
	// Enabled/CapabilityID/InstallationID fields (no legacy Kind/IsDefault columns)
	// must still satisfy the auto-approve gate.
	store.integrations = []Integration{routerInst("router-1")}
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{})

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	// The configured router connection auto-approves and immediately submits, so the
	// request lands in the fulfillment pipeline (one queued target).
	if req.Status != StatusQueued {
		t.Fatalf("status = %q, want queued (auto-approved and submitted)", req.Status)
	}
}

func TestCreateRequestAutoApprovalRespectsSupportedMediaTypes(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	// A router connection that only serves series must NOT auto-approve a movie
	// request; the gate falls back to manual approval (pending).
	seriesOnly := routerInst("router-series")
	seriesOnly.SupportedMediaTypes = []string{string(MediaTypeSeries)}
	store.integrations = []Integration{seriesOnly}
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{})

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusPending {
		t.Fatalf("status = %q, want pending (no router connection supports movie)", req.Status)
	}
}

func TestCreateRequestAutoApprovalSubmitsMovie(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	// The repo decrypts api_key_ref on read, so the connection carries the literal
	// key here (no host-side secret resolution).
	store.integrations = []Integration{autoApproveRouterInst("router-1", "radarr-key")}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusQueued || req.ExternalID != "ext-1080p" {
		t.Fatalf("request = %+v, want queued with router external id", req)
	}
	if router.fulfillCalls != 1 {
		t.Fatalf("fulfill calls = %d, want 1", router.fulfillCalls)
	}
	// The plaintext credential is resolved before dispatch and handed to the provider.
	if len(router.gotConns) != 1 || router.gotConns[0].APIKey != "radarr-key" {
		t.Fatalf("router connections = %+v, want resolved api key", router.gotConns)
	}
}

func TestCreateRequestSubmissionFailureMarksFailed(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	store.integrations = []Integration{autoApproveRouterInst("router-1", "radarr-key")}
	// A provider that creates no targets (e.g. no radarr instance) returns its own
	// message; the host marks the request failed with it.
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{noTargets: true, fulfillMsg: "radarr unavailable"})

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Outcome != OutcomeFailed || req.LastError != "radarr unavailable" {
		t.Fatalf("request = %+v, want failed outcome with provider message", req)
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

func TestListMineAttachesTargets(t *testing.T) {
	store := newFakeStore()
	store.mine = []*Request{{
		ID:                "req-1",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Status:            StatusQueued,
		Outcome:           OutcomeActive,
		RequestedByUserID: 1,
	}}
	store.targets = map[string][]Target{
		"req-1": {{
			ID:        10,
			RequestID: "req-1",
			Quality:   Quality2160p,
			Status:    StatusQueued,
		}},
	}

	got, err := newTestService(store).ListMine(context.Background(), testViewer(1), ListFilter{})
	if err != nil {
		t.Fatalf("ListMine returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListMine returned %d requests, want 1", len(got))
	}
	if len(got[0].Targets) != 1 || got[0].Targets[0].Quality != Quality2160p {
		t.Fatalf("targets = %+v, want attached 2160p target", got[0].Targets)
	}
}

func TestListMineAttachesLibraryContentID(t *testing.T) {
	store := newFakeStore()
	store.mine = []*Request{{
		ID:                "req-1",
		Provider:          "tmdb",
		MediaType:         MediaTypeMovie,
		TMDBID:            42,
		Title:             "Test Movie",
		Status:            StatusCompleted,
		Outcome:           OutcomeActive,
		RequestedByUserID: 1,
	}}
	presence := &fakePresence{available: map[MediaType]map[int]bool{
		MediaTypeMovie: {42: true},
	}}

	got, err := NewService(store, &fakeTMDBClient{}, presence).ListMine(context.Background(), testViewer(1), ListFilter{})
	if err != nil {
		t.Fatalf("ListMine returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListMine returned %d requests, want 1", len(got))
	}
	if got[0].LibraryContentID != "movie-42" {
		t.Fatalf("library content id = %q, want movie-42", got[0].LibraryContentID)
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
	if got := page.Results[0].LibraryContentID; got != "series-201992" {
		t.Fatalf("library content id = %q, want series-201992", got)
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

func TestReconcileRequestsMarksDownloadingFromProvider(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.candidates = []*Request{{
		ID:        "req-1",
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Status:    StatusQueued,
		Outcome:   OutcomeActive,
	}}
	// Reconcile drives status per-target via the provider; seed a queued target.
	store.requests["req-1"] = &Request{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeActive}
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: "req-1", IntegrationID: "router-1",
		Quality: Quality1080p, Status: StatusQueued, ExternalID: "123",
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{{
		Quality:        Quality1080p,
		ConnectionID:   "router-1",
		Status:         StatusDownloading,
		ExternalStatus: "downloading",
	}}}
	service := newTestService(store)
	service.SetRouterProvider(router)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Downloading != 1 || len(store.statusUpdates) != 1 || store.statusUpdates[0] != StatusDownloading {
		t.Fatalf("result = %+v statusUpdates = %+v, want one downloading update", result, store.statusUpdates)
	}
	if router.statusCalls != 1 {
		t.Fatalf("provider status calls = %d, want 1", router.statusCalls)
	}
}

func TestReconcileRequestsPreservesProviderFailureMessage(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.candidates = []*Request{{
		ID:        "req-1",
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Status:    StatusQueued,
		Outcome:   OutcomeActive,
	}}
	store.requests["req-1"] = &Request{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeActive}
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: "req-1", IntegrationID: "router-1",
		Quality: Quality1080p, Status: StatusQueued, ExternalID: "123",
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{{
		Quality:        Quality1080p,
		ConnectionID:   "router-1",
		Status:         StatusFailed,
		ExternalStatus: "failed",
		Message:        "indexer rejected the request",
	}}}
	service := newTestService(store)
	service.SetRouterProvider(router)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("result = %+v, want one failed target update", result)
	}
	targets, _ := store.ListTargets(context.Background(), "req-1")
	if len(targets) != 1 || targets[0].LastError != "indexer rejected the request" {
		t.Fatalf("targets = %+v, want provider failure message preserved", targets)
	}
}

func TestReconcileRequestsResolvesGlobalInputsOncePerCycle(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")} // APIKeyRef "key-router-1"

	// Three approved candidates that all share the same router connection. Each
	// one drives submitApprovedRequest, which resolves the router connection.
	// Without per-cycle caching this would fetch integrations/settings once per
	// request.
	for _, id := range []string{"req-1", "req-2", "req-3"} {
		req := &Request{ID: id, MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive}
		store.candidates = append(store.candidates, req)
		store.requests[id] = req
	}

	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{})

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Checked != 3 || result.Submitted != 3 {
		t.Fatalf("result = %+v, want 3 checked / 3 submitted", result)
	}

	// Integrations and settings are fetched once per cycle (the connection's key is
	// already decrypted by the repo on read, so there is nothing to re-resolve).
	if store.listIntegrationsCalls != 1 {
		t.Fatalf("ListIntegrations calls = %d, want 1 per cycle", store.listIntegrationsCalls)
	}
	if store.getSettingsCalls != 1 {
		t.Fatalf("GetSettings calls = %d, want 1 per cycle", store.getSettingsCalls)
	}
}

func TestDeleteIntegrationRejectsLiveTargets(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{{
		ID:      "radarr-hd",
		Enabled: true,
	}}
	store.targets = map[string][]Target{
		"req-1": {{
			ID:            10,
			RequestID:     "req-1",
			IntegrationID: "radarr-hd",
			Quality:       Quality1080p,
			Status:        StatusDownloading,
		}},
	}

	err := newTestService(store).DeleteIntegration(
		context.Background(),
		Viewer{UserID: 1, IsAdmin: true},
		"radarr-hd",
	)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState", err)
	}
	if len(store.integrations) != 1 {
		t.Fatalf("integrations = %d, want delete blocked", len(store.integrations))
	}
}

func TestCreateIntegrationRejectedByPluginValidate(t *testing.T) {
	store := newFakeStore()
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{
		validateFieldErrors: map[string]string{"root_folder": "root folder does not exist"},
		validateFormError:   "connection invalid",
	})

	install := 1
	_, err := service.CreateIntegration(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		Name:           "radarr",
		CapabilityID:   "arr",
		BaseURL:        "http://radarr.local",
		InstallationID: &install,
	})
	if err == nil {
		t.Fatal("expected validation error from plugin Validate")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if ve.FieldErrors["root_folder"] != "root folder does not exist" {
		t.Fatalf("field errors = %+v, want root_folder error", ve.FieldErrors)
	}
	if ve.FormError != "connection invalid" {
		t.Fatalf("form error = %q, want connection invalid", ve.FormError)
	}
	if len(store.integrations) != 0 {
		t.Fatalf("integrations = %d, want 0 (rejected before persist)", len(store.integrations))
	}
}

// TestValidateInstanceRequiresCapabilitySubID locks the capability_id contract:
// the column carries the capability SUB-ID ("arr"/"seerr"), matching the value
// the host passes to pluginhost.Client.RequestRouter -> requireCapability, which
// keys on (type, id). The capability TYPE ("request_router.v1") must NOT be
// accepted or defaulted in, since requireCapability("request_router.v1",
// "request_router.v1") never matches a plugin whose capability id is "arr".
func TestValidateInstanceRequiresCapabilitySubID(t *testing.T) {
	install := 1
	if err := validateInstance(&Integration{Name: "radarr", CapabilityID: "arr", InstallationID: &install}); err != nil {
		t.Fatalf("validateInstance(sub-id \"arr\") = %v, want nil", err)
	}
	if err := validateInstance(&Integration{Name: "radarr", CapabilityID: "", InstallationID: &install}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("validateInstance(empty capability) = %v, want ErrInvalidInput", err)
	}
}

// TestCreateIntegrationPassesCapabilitySubIDToPlugin guards that the sub-id the
// admin selected reaches the router provider verbatim (it used to be rewritten to
// the capability type, which the plugin runtime could never resolve).
func TestCreateIntegrationPassesCapabilitySubIDToPlugin(t *testing.T) {
	store := newFakeStore()
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	install := 1
	if _, err := service.CreateIntegration(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		Name:           "radarr",
		CapabilityID:   "arr",
		BaseURL:        "http://radarr.local",
		APIKeyRef:      "key-radarr",
		InstallationID: &install,
	}); err != nil {
		t.Fatalf("CreateIntegration err = %v, want nil", err)
	}
	if router.gotValidateCapability != "arr" {
		t.Fatalf("plugin Validate capability = %q, want \"arr\"", router.gotValidateCapability)
	}
}

// TestUpdateIntegrationRefusesStoredKeyReuseOnChangedBaseURL covers the security
// hardening: when the caller leaves api_key_ref blank ("keep saved key") but
// changes the base_url, the service must refuse rather than pair the stored,
// API-unreadable key with the new (potentially attacker-controlled) URL. The
// plugin Validate is never called and nothing is persisted.
func TestUpdateIntegrationRefusesStoredKeyReuseOnChangedBaseURL(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")} // BaseURL http://router-1.local, APIKeyRef key-router-1
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	install := 1
	_, err := service.UpdateIntegration(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:             "router-1",
		Name:           "router-1",
		CapabilityID:   "arr",
		BaseURL:        "http://attacker.example", // changed from the stored base URL
		APIKeyRef:      "",                        // blank -> "keep saved key"
		InstallationID: &install,
	})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if ve.FieldErrors["api_key_ref"] == "" {
		t.Fatalf("field errors = %+v, want api_key_ref message", ve.FieldErrors)
	}
	if router.validateCalls != 0 {
		t.Fatalf("plugin Validate calls = %d, want 0 (refused before dispatch)", router.validateCalls)
	}
	// Stored row must be unchanged (not persisted with the new URL).
	if got := store.integrations[0].BaseURL; got != "http://router-1.local" {
		t.Fatalf("stored base_url = %q, want unchanged http://router-1.local", got)
	}
}

// TestUpdateIntegrationKeepsKeyWhenBaseURLUnchanged confirms the normal
// edit-keeping-key flow still works: blank api_key_ref with an unchanged (or
// blank) base_url backfills the stored key, calls the plugin Validate, and
// persists.
func TestUpdateIntegrationKeepsKeyWhenBaseURLUnchanged(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	install := 1
	updated, err := service.UpdateIntegration(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:             "router-1",
		Name:           "router-1-renamed",
		CapabilityID:   "arr",
		BaseURL:        "http://router-1.local", // unchanged
		APIKeyRef:      "",                      // blank -> keep saved key
		InstallationID: &install,
	})
	if err != nil {
		t.Fatalf("UpdateIntegration err = %v, want nil", err)
	}
	if router.validateCalls != 1 {
		t.Fatalf("plugin Validate calls = %d, want 1", router.validateCalls)
	}
	if updated == nil || updated.Name != "router-1-renamed" {
		t.Fatalf("updated = %+v, want persisted name router-1-renamed", updated)
	}
}

func TestLoadIntegrationOptionsDoesNotBackfillStoredKeyForChangedBaseURL(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	if _, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:      "router-1",
		BaseURL: "http://attacker.example",
	}); err != nil {
		t.Fatalf("LoadIntegrationOptions: %v", err)
	}
	if router.gotOptionsConn.APIKey != "" {
		t.Fatalf("probe API key = %q, want empty for changed base URL", router.gotOptionsConn.APIKey)
	}
	if router.gotOptionsConn.BaseURL != "http://attacker.example" {
		t.Fatalf("probe base URL = %q, want submitted URL", router.gotOptionsConn.BaseURL)
	}
}

func TestLoadIntegrationOptionsBackfillsStoredKeyForSameBaseURL(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	if _, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:      "router-1",
		BaseURL: "http://router-1.local",
	}); err != nil {
		t.Fatalf("LoadIntegrationOptions: %v", err)
	}
	if router.gotOptionsConn.APIKey != "key-router-1" {
		t.Fatalf("probe API key = %q, want stored key for unchanged base URL", router.gotOptionsConn.APIKey)
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
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["req-1"] = &Request{
		ID:        "req-1",
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Status:    StatusQueued,
		Outcome:   OutcomeFailed,
	}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	req, err := service.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1")
	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if router.fulfillCalls != 1 {
		t.Fatalf("fulfill calls = %d, want 1", router.fulfillCalls)
	}
	// Retry transitions the failed request back to approved and re-dispatches via
	// the router; the request aggregate returns to queued with the new external id.
	if req.Status != StatusQueued || req.ExternalID != "ext-1080p" {
		t.Fatalf("request = %+v, want re-queued with router external id", req)
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
	candidates    []*Request
	mine          []*Request
	statusUpdates []Status
	requests      map[string]*Request
	targets       map[string][]Target
	targetSeq     int64

	listIntegrationsCalls int
	getSettingsCalls      int
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
	f.getSettingsCalls++
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
	req := &Request{
		ID:                   input.ID,
		Provider:             "tmdb",
		MediaType:            input.Input.MediaType,
		TMDBID:               input.Input.TMDBID,
		TVDBID:               input.Input.TVDBID,
		IMDbID:               input.Input.IMDbID,
		Title:                input.Input.Title,
		Status:               input.Status,
		Outcome:              input.Outcome,
		IsAnime:              input.IsAnime,
		RequestedByUserID:    input.Requester.UserID,
		RequestedByProfileID: input.Requester.ProfileID,
		CreatedAt:            input.Now,
		UpdatedAt:            input.Now,
	}
	f.requests[input.ID] = req
	copy := *req
	return &copy, nil
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
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*Request(nil), f.mine...), nil
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
	f.listIntegrationsCalls++
	return f.integrations, nil
}

func (f *fakeStore) GetIntegration(_ context.Context, id string) (*Integration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.integrations {
		if f.integrations[i].ID == id {
			cp := f.integrations[i]
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeStore) CreateIntegration(_ context.Context, in Integration) (*Integration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.integrations = append(f.integrations, in)
	cp := in
	return &cp, nil
}

func (f *fakeStore) UpdateIntegration(_ context.Context, in Integration) (*Integration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.integrations {
		if f.integrations[i].ID == in.ID {
			f.integrations[i] = in
			cp := in
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeStore) SaveIntegrationWithDefaults(_ context.Context, in Integration, isCreate bool) (*Integration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if isCreate {
		f.integrations = append(f.integrations, in)
		cp := in
		return &cp, nil
	}
	for i := range f.integrations {
		if f.integrations[i].ID == in.ID {
			f.integrations[i] = in
			cp := in
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeStore) DeleteIntegration(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, targets := range f.targets {
		for _, target := range targets {
			if target.IntegrationID == id && (target.Status == StatusQueued || target.Status == StatusDownloading) {
				return ErrInvalidState
			}
		}
	}
	for i := range f.integrations {
		if f.integrations[i].ID == id {
			f.integrations = append(f.integrations[:i], f.integrations[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeStore) ListTargets(_ context.Context, requestID string) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Target(nil), f.targets[requestID]...), nil
}

func (f *fakeStore) CreateTarget(_ context.Context, t Target) (Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.targets == nil {
		f.targets = map[string][]Target{}
	}
	f.targetSeq++
	t.ID = f.targetSeq
	f.targets[t.RequestID] = append(f.targets[t.RequestID], t)
	return t, nil
}

func (f *fakeStore) DeleteTarget(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for rid, ts := range f.targets {
		for i := range ts {
			if ts[i].ID == id {
				f.targets[rid] = append(ts[:i], ts[i+1:]...)
				return nil
			}
		}
	}
	return ErrNotFound
}

func (f *fakeStore) UpdateTargetStatus(_ context.Context, targetID int64, status Status, externalID, externalStatus, lastErr string, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var requestID string
	for rid, ts := range f.targets {
		for i := range ts {
			if ts[i].ID == targetID {
				if externalID != "" {
					f.targets[rid][i].ExternalID = externalID
				}
				if externalStatus != "" {
					f.targets[rid][i].ExternalStatus = externalStatus
				}
				f.targets[rid][i].Status = status
				f.targets[rid][i].LastError = lastErr
				requestID = rid
			}
		}
	}
	if requestID == "" {
		return nil, ErrNotFound
	}
	f.statusUpdates = append(f.statusUpdates, status)
	st, outcome := aggregateStatus(f.targets[requestID])
	req := f.requests[requestID]
	if req == nil {
		req = &Request{ID: requestID}
		f.requests[requestID] = req
	}
	req.Status = st
	req.Outcome = outcome
	// Surface the first target's external identity on the request snapshot so
	// existing assertions on req.ExternalID/IntegrationKind keep working.
	for _, t := range f.targets[requestID] {
		if t.ExternalID != "" {
			req.ExternalID = t.ExternalID
			req.ExternalStatus = t.ExternalStatus
			req.IntegrationKind = t.IntegrationKind
			break
		}
	}
	if outcome == OutcomeFailed {
		req.LastError = lastErr
	}
	copy := *req
	return &copy, nil
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
			out[candidate.TMDBID] = PresenceMatch{
				Available:       true,
				ContentID:       fakePresenceContentID(mediaType, candidate.TMDBID),
				MatchedProvider: "tmdb",
			}
			continue
		}
		if candidate.TVDBID != nil && f.byTVDB != nil {
			if tmdbID, ok := f.byTVDB[mediaType][*candidate.TVDBID]; ok && tmdbID == candidate.TMDBID {
				out[candidate.TMDBID] = PresenceMatch{
					Available:       true,
					ContentID:       fakePresenceContentID(mediaType, candidate.TMDBID),
					MatchedProvider: "tvdb",
				}
			}
		}
	}
	return out, nil
}

func fakePresenceContentID(mediaType MediaType, tmdbID int) string {
	return fmt.Sprintf("%s-%d", mediaType, tmdbID)
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

type fixedCeiling struct{ q string }

func (f fixedCeiling) MaxPlaybackQuality(context.Context, int, string) (string, error) {
	return f.q, nil
}

// fakeRouterProvider is a canned RequestRouterProvider standing in for a
// request_router.v1 plugin. Fulfill emits one target per requested quality
// (unless noTargets is set), recording the qualities and connections it saw.
type fakeRouterProvider struct {
	mu sync.Mutex

	// Fulfill behavior.
	noTargets         bool
	fulfillMsg        string
	fulfillErr        error
	targetsOverride   []RouterTarget // when non-nil, Fulfill returns this verbatim
	gotQualities      []Quality
	gotConns          []ResolvedRouterConnection
	gotInstallationID int
	fulfillCalls      int

	gotRequesterEmail    string
	gotRequesterUsername string

	// CheckStatus behavior.
	statuses    []RouterTargetStatus
	statusErr   error
	statusCalls int

	// ListConfigOptions behavior.
	options        map[string][]RouterOption
	gotOptionsConn ResolvedRouterConnection

	// Validate behavior (default empty = valid).
	validateFieldErrors   map[string]string
	validateFormError     string
	validateErr           error
	validateCalls         int
	gotValidateCapability string
	gotValidateSiblings   []ResolvedRouterConnection
}

func (f *fakeRouterProvider) Fulfill(_ context.Context, installationID int, _ string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotRequesterEmail = req.RequesterEmail
	f.gotRequesterUsername = req.RequesterUsername
	f.fulfillCalls++
	f.gotQualities = append(f.gotQualities, qualities...)
	f.gotConns = conns
	f.gotInstallationID = installationID
	if f.fulfillErr != nil {
		return nil, "", f.fulfillErr
	}
	if f.targetsOverride != nil {
		return f.targetsOverride, f.fulfillMsg, nil
	}
	if f.noTargets {
		return nil, f.fulfillMsg, nil
	}
	connID := ""
	if len(conns) > 0 {
		connID = conns[0].ID
	}
	out := make([]RouterTarget, 0, len(qualities))
	for _, q := range qualities {
		out = append(out, RouterTarget{
			Quality:        q,
			ConnectionID:   connID,
			ExternalID:     "ext-" + string(q),
			ExternalStatus: "queued",
			Status:         StatusQueued,
		})
	}
	return out, f.fulfillMsg, nil
}

func (f *fakeRouterProvider) CheckStatus(_ context.Context, _ int, _ string, _ Request, _ []RouterTargetRef, _ []ResolvedRouterConnection) ([]RouterTargetStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return f.statuses, f.statusErr
}

func (f *fakeRouterProvider) ListConfigOptions(_ context.Context, _ int, _ string, conn ResolvedRouterConnection) (map[string][]RouterOption, error) {
	f.mu.Lock()
	f.gotOptionsConn = conn
	f.mu.Unlock()
	return f.options, nil
}

func (f *fakeRouterProvider) TestConnection(_ context.Context, _ int, _ string, _ ResolvedRouterConnection) (bool, string, error) {
	return true, "", nil
}

func (f *fakeRouterProvider) Validate(_ context.Context, _ int, capabilityID string, _ ResolvedRouterConnection, siblings []ResolvedRouterConnection) (map[string]string, string, error) {
	f.mu.Lock()
	f.validateCalls++
	f.gotValidateCapability = capabilityID
	f.gotValidateSiblings = siblings
	f.mu.Unlock()
	return f.validateFieldErrors, f.validateFormError, f.validateErr
}

// routerInst builds an enabled request_router integration connection for tests.
func routerInst(id string) Integration {
	return routerInstOn(id, 1)
}

// routerInstOn builds an enabled request_router connection bound to a specific
// installation id (for multi-installation isolation tests).
func routerInstOn(id string, installID int) Integration {
	install := installID
	return Integration{
		ID:             id,
		Name:           id,
		Enabled:        true,
		BaseURL:        "http://" + id + ".local",
		APIKeyRef:      "key-" + id,
		CapabilityID:   "arr",
		InstallationID: &install,
	}
}

// autoApproveRouterInst is a router connection that satisfies the auto-approval
// gate (integrationConfigured: an enabled request_router connection bound to an
// installation with a base URL and api key).
func autoApproveRouterInst(id, apiKeyRef string) Integration {
	in := routerInst(id)
	in.APIKeyRef = apiKeyRef
	return in
}

func TestUpdateIntegrationPassesSiblingsToValidate(t *testing.T) {
	store := newFakeStore()
	inst := 1
	a := routerInstOn("conn-a", inst)
	b := routerInstOn("conn-b", inst)
	b.PluginConfig = map[string]any{"service_kind": "radarr"}
	store.integrations = []Integration{a, b}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	if _, err := service.UpdateIntegration(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:             "conn-a",
		Name:           "conn-a",
		CapabilityID:   "arr",
		BaseURL:        "http://conn-a.local",
		APIKeyRef:      "key-conn-a",
		InstallationID: &inst,
	}); err != nil {
		t.Fatalf("UpdateIntegration: %v", err)
	}
	if len(router.gotValidateSiblings) != 1 {
		t.Fatalf("siblings = %d, want 1 (the other installation-1 connection)", len(router.gotValidateSiblings))
	}
	sib := router.gotValidateSiblings[0]
	if sib.ID != "conn-b" {
		t.Fatalf("sibling id = %q, want conn-b (self excluded)", sib.ID)
	}
	if sib.APIKey != "" || sib.BaseURL != "" {
		t.Fatalf("sibling must carry no credentials, got APIKey=%q BaseURL=%q", sib.APIKey, sib.BaseURL)
	}
	if sib.Config["service_kind"] != "radarr" {
		t.Fatalf("sibling config not passed: %+v", sib.Config)
	}
}

func TestAllowedQualities(t *testing.T) {
	svc := newTestService(newFakeStore())

	t.Run("hd ceiling stays 1080p only", func(t *testing.T) {
		svcHD := newTestService(newFakeStore())
		svcHD.SetEntitlementResolver(fixedCeiling{q: "1080p"})
		got := svcHD.allowedQualities(context.Background(), Request{}, Settings{})
		if len(got) != 1 || got[0] != Quality1080p {
			t.Fatalf("qualities = %v, want [1080p]", got)
		}
	})

	t.Run("any/no-cap ceiling adds 2160p", func(t *testing.T) {
		// A requester whose max playback quality is "Any" resolves to an empty
		// (no-cap) ceiling. Empty means UNLIMITED, so 4K must be requested
		// alongside 1080p — it must not be read as "below 4K".
		svcAny := newTestService(newFakeStore())
		svcAny.SetEntitlementResolver(fixedCeiling{q: ""})
		got := svcAny.allowedQualities(context.Background(), Request{}, Settings{})
		if len(got) != 2 || got[1] != Quality2160p {
			t.Fatalf("qualities = %v, want [1080p 2160p]", got)
		}
	})

	t.Run("force dual adds 2160p", func(t *testing.T) {
		got := svc.allowedQualities(context.Background(), Request{}, Settings{ForceDualQuality: true})
		if len(got) != 2 || got[1] != Quality2160p {
			t.Fatalf("qualities = %v, want [1080p 2160p]", got)
		}
	})

	t.Run("4k ceiling adds 2160p", func(t *testing.T) {
		svc4k := newTestService(newFakeStore())
		svc4k.SetEntitlementResolver(fixedCeiling{q: "2160p"})
		got := svc4k.allowedQualities(context.Background(), Request{}, Settings{})
		if len(got) != 2 || got[1] != Quality2160p {
			t.Fatalf("qualities = %v, want [1080p 2160p]", got)
		}
	})
}

func TestSubmitApprovedFansOutDualQuality(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{}
	svc := NewService(store, &fakeTMDBClient{}, &fakePresence{})
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(router.gotQualities) != 2 {
		t.Fatalf("expected 2 qualities (hd+uhd), got %d: %v", len(router.gotQualities), router.gotQualities)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 2 {
		t.Fatalf("expected 2 persisted targets, got %d", len(targets))
	}
}

func TestSubmitApprovedSkipsUnconfiguredOptional4KTarget(t *testing.T) {
	store := newFakeStore()
	hd := routerInst("router-1")
	hd.PluginConfig = map[string]any{
		"service_kind": "radarr",
		"is_default":   true,
		"is_4k":        false,
	}
	store.integrations = []Integration{hd}
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: ""})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(router.gotQualities) != 1 || router.gotQualities[0] != Quality1080p {
		t.Fatalf("qualities = %v, want only [1080p] when no 4K default is configured", router.gotQualities)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 || targets[0].Quality != Quality1080p || targets[0].Status == StatusFailed {
		t.Fatalf("targets = %+v, want one healthy 1080p target", targets)
	}
}

func TestSubmitApprovedUsesConfiguredOptional4KDefault(t *testing.T) {
	store := newFakeStore()
	hd := routerInst("router-hd")
	hd.PluginConfig = map[string]any{
		"service_kind": "radarr",
		"is_default":   true,
		"is_4k":        false,
	}
	uhd := routerInst("router-uhd")
	uhd.PluginConfig = map[string]any{
		"service_kind":  "radarr",
		"is_4k":         true,
		"is_default_4k": true,
	}
	store.integrations = []Integration{hd, uhd}
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(router.gotQualities) != 2 || router.gotQualities[0] != Quality1080p || router.gotQualities[1] != Quality2160p {
		t.Fatalf("qualities = %v, want [1080p 2160p]", router.gotQualities)
	}
}

func TestSubmitApprovedNoRouterFails(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	svc := newTestService(store) // no router provider set

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want failed (no router configured)", got.Outcome)
	}
}

func TestSubmitApprovedNoConnectionsFails(t *testing.T) {
	store := newFakeStore() // no integrations
	svc := newTestService(store)
	svc.SetRouterProvider(&fakeRouterProvider{})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want failed (no enabled router connections)", got.Outcome)
	}
}

func TestSubmitApprovedZeroTargetsUsesProviderMessage(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	svc := newTestService(store)
	svc.SetRouterProvider(&fakeRouterProvider{noTargets: true, fulfillMsg: "no radarr instance configured for 1080p"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.Outcome != OutcomeFailed || got.LastError != "no radarr instance configured for 1080p" {
		t.Fatalf("request = %+v, want failed with provider message", got)
	}
}

func TestSubmitApprovedIsIdempotentPerQuality(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: "1080p"}) // HD ceiling -> only 1080p allowed

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	// Seed a healthy 1080p target so the re-run should not re-submit that quality.
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: "r1", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusQueued, ExternalID: "ext-existing",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	// HD ceiling only -> only 1080p is allowed, and it already has a healthy target,
	// so Fulfill is never called.
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want 0 (healthy 1080p target already exists)", router.fulfillCalls)
	}
}

func TestSubmitApprovedRecordsDroppedQualityAsFailed(t *testing.T) {
	store := newFakeStore()
	store.settings.ForceDualQuality = true // want both 1080p and 2160p
	store.integrations = []Integration{routerInst("router-1")}
	// Plugin fulfills only 1080p, dropping the wanted 2160p.
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{{
		Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "ext-hd", ExternalStatus: "queued", Status: StatusQueued,
	}}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}

	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2 (1080p queued + 2160p failed)", len(targets))
	}
	var failed2160 *Target
	for i := range targets {
		if targets[i].Quality == Quality2160p {
			failed2160 = &targets[i]
		}
	}
	if failed2160 == nil || failed2160.Status != StatusFailed {
		t.Fatalf("2160p target = %+v, want a failed target", failed2160)
	}
	if failed2160.LastError != "fulfillment backend returned no target for this quality" {
		t.Fatalf("2160p last error = %q, want the no-target message", failed2160.LastError)
	}

	// The failed 2160p target is not "healthy", so a re-run (Retry / reconcile)
	// re-attempts only that quality. Provide a normal provider for the re-run.
	retryRouter := &fakeRouterProvider{}
	svc.SetRouterProvider(retryRouter)
	cur := *store.requests["r1"]
	cur.Status = StatusApproved
	cur.Outcome = OutcomeActive
	if _, err := svc.submitApprovedRequest(context.Background(), cur, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("retry submit: %v", err)
	}
	if len(retryRouter.gotQualities) != 1 || retryRouter.gotQualities[0] != Quality2160p {
		t.Fatalf("retry qualities = %v, want only [2160p] (1080p is healthy)", retryRouter.gotQualities)
	}
}

func TestSubmitApprovedContainsToSingleInstallation(t *testing.T) {
	store := newFakeStore()
	// Two enabled router connections on DIFFERENT installations. Only the first
	// installation's connections may be sent to that plugin.
	store.integrations = []Integration{
		routerInstOn("router-a", 1),
		routerInstOn("router-b", 2),
	}
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.gotInstallationID != 1 {
		t.Fatalf("installation id = %d, want 1 (first eligible)", router.gotInstallationID)
	}
	if len(router.gotConns) != 1 || router.gotConns[0].ID != "router-a" {
		t.Fatalf("connections = %+v, want only installation 1's router-a", router.gotConns)
	}
}

// TestSubmitApprovedContainsToChosenCapability guards that when one installation
// exposes connections for more than one request_router capability sub-id, the
// host hands the plugin only the connections for the FIRST chosen capability —
// never a connection belonging to a different capability of the same installation.
func TestSubmitApprovedContainsToChosenCapability(t *testing.T) {
	store := newFakeStore()
	arrConn := routerInstOn("arr-conn", 1) // CapabilityID "arr"
	seerrConn := routerInstOn("seerr-conn", 1)
	seerrConn.CapabilityID = "seerr"
	store.integrations = []Integration{arrConn, seerrConn}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)
	service.SetEntitlementResolver(fixedCeiling{q: "1080p"}) // single quality, keep it simple

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := service.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(router.gotConns) != 1 {
		t.Fatalf("fulfill conns = %d, want 1 (contained to the first chosen capability, not mixed across arr+seerr)", len(router.gotConns))
	}
	if router.gotConns[0].ID != "arr-conn" {
		t.Fatalf("fulfilled conn = %q, want arr-conn (first chosen)", router.gotConns[0].ID)
	}
}

func TestSubmitApprovedDedupesDuplicateQualityTargets(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	// Misbehaving plugin returns two targets for the same quality.
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{
		{Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "ext-1", Status: StatusQueued},
		{Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "ext-2", Status: StatusQueued},
	}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	// Pin an HD ceiling: this test is about deduping a duplicate quality, not 4K
	// entitlement, so keep it to a single requested quality (1080p).
	svc.SetEntitlementResolver(fixedCeiling{q: "1080p"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit returned error: %v", err)
	}
	if got.Outcome == OutcomeFailed {
		t.Fatalf("outcome = failed, want a clean queued aggregate")
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1 (duplicate quality deduped)", len(targets))
	}
}

func TestSubmitApprovedSkipsMismatchedMediaType(t *testing.T) {
	store := newFakeStore()
	// Only a series-serving router connection exists; a movie request must not use it.
	seriesOnly := routerInst("router-series")
	seriesOnly.SupportedMediaTypes = []string{string(MediaTypeSeries)}
	store.integrations = []Integration{seriesOnly}
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.Outcome != OutcomeFailed || got.LastError != "no fulfillment backend configured" {
		t.Fatalf("request = %+v, want failed with no-backend message", got)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want 0 (series connection filtered out for a movie)", router.fulfillCalls)
	}
}

func TestSubmitApprovedSkipsBadConnectionUsesSibling(t *testing.T) {
	store := newFakeStore()
	// Two connections on the same installation: one has no api key (unconfigured),
	// the other carries a literal key. The healthy sibling must still fulfill the
	// request, and the no-key connection must never pin the installation.
	bad := routerInstOn("router-bad", 1)
	bad.APIKeyRef = ""
	good := routerInstOn("router-good", 1)
	good.APIKeyRef = "good-key"
	store.integrations = []Integration{bad, good}
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit must not abort on a single bad connection: %v", err)
	}
	if got.Outcome == OutcomeFailed {
		t.Fatalf("outcome = failed, want submitted via the healthy sibling")
	}
	if router.fulfillCalls != 1 {
		t.Fatalf("fulfill calls = %d, want 1", router.fulfillCalls)
	}
	if len(router.gotConns) != 1 || router.gotConns[0].ID != "router-good" || router.gotConns[0].APIKey != "good-key" {
		t.Fatalf("router connections = %+v, want only the healthy router-good with resolved key", router.gotConns)
	}
}

func TestSubmitApprovedSkipsConnectionWithEmptyKey(t *testing.T) {
	store := newFakeStore()
	noKey := routerInstOn("router-nokey", 1)
	noKey.APIKeyRef = "" // resolves empty -> must be skipped (never send unauthenticated)
	store.integrations = []Integration{noKey}
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want failed (no usable connection)", got.Outcome)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want 0 (empty-key connection skipped)", router.fulfillCalls)
	}
}

func TestSubmitApprovedSkipsUnknownQuality(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	// Plugin returns a bogus quality alongside a valid one.
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{
		{Quality: Quality("720p"), ConnectionID: "router-1", ExternalID: "ext-bad", Status: StatusQueued},
		{Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "ext-hd", Status: StatusQueued},
	}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	// Pin an HD ceiling: this test is about skipping an unknown quality, not 4K
	// entitlement, so keep it to a single requested quality (1080p).
	svc.SetEntitlementResolver(fixedCeiling{q: "1080p"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1 (720p skipped, 1080p persisted)", len(targets))
	}
	for _, tg := range targets {
		if tg.Quality == Quality("720p") {
			t.Fatalf("a 720p target was persisted: %+v", tg)
		}
	}
}

func TestSubmitApprovedSkipsUnknownConnectionTarget(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{
		{Quality: Quality1080p, ConnectionID: "missing-router", ExternalID: "ext-hd", Status: StatusQueued},
	}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: "1080p"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit must not abort on an unknown plugin connection: %v", err)
	}
	if got.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want failed missing-quality target", got.Outcome)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 || targets[0].IntegrationID != "" || targets[0].Status != StatusFailed {
		t.Fatalf("targets = %+v, want one failed target without unknown integration id", targets)
	}
}

func TestSubmitApprovedCoercesUnknownStatusToQueued(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{
		{Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "ext-hd", Status: Status("bogus")},
	}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	// Pin an HD ceiling: this test is about status coercion, not 4K entitlement,
	// so keep it to a single requested quality (1080p).
	svc.SetEntitlementResolver(fixedCeiling{q: "1080p"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 || targets[0].Status != StatusQueued {
		t.Fatalf("targets = %+v, want one StatusQueued target (unknown status coerced)", targets)
	}
}

func TestSubmitApprovedSkipsTargetForHealthyQuality(t *testing.T) {
	store := newFakeStore()
	store.settings.ForceDualQuality = true // want 1080p + 2160p
	store.integrations = []Integration{routerInst("router-1")}
	// Seed a healthy 1080p target; the plugin (misbehaving) returns one anyway.
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: "r1", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusQueued, ExternalID: "ext-existing",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{
		{Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "ext-dupe", Status: StatusQueued},
		{Quality: Quality2160p, ConnectionID: "router-1", ExternalID: "ext-uhd", Status: StatusQueued},
	}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit must not error on a duplicate of a healthy quality: %v", err)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2 (existing 1080p kept + new 2160p; dup 1080p skipped)", len(targets))
	}
	count1080 := 0
	for _, tg := range targets {
		if tg.Quality == Quality1080p {
			count1080++
		}
	}
	if count1080 != 1 {
		t.Fatalf("1080p targets = %d, want 1 (no duplicate persisted)", count1080)
	}
}

func TestSubmitApprovedUnboundInstallationFailsWithGuidance(t *testing.T) {
	store := newFakeStore()
	// A router connection that exists but is not bound to a plugin installation
	// (the migration leaves installation_id NULL for pre-existing rows).
	unbound := routerInst("router-unbound")
	unbound.InstallationID = nil
	store.integrations = []Integration{unbound}
	svc := newTestService(store)
	svc.SetRouterProvider(&fakeRouterProvider{})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.Outcome != OutcomeFailed ||
		got.LastError != "request backend connection is not bound to a plugin installation; re-save it in admin" {
		t.Fatalf("request = %+v, want failed with unbound-installation guidance", got)
	}
}

type fakeRequesterIdentity struct {
	email, username string
	err             error
	gotUserID       int
}

func (f *fakeRequesterIdentity) ResolveRequester(_ context.Context, userID int) (string, string, error) {
	f.gotUserID = userID
	return f.email, f.username, f.err
}

// TestApprove_PublishesRequestApproved checks that approving a pending request
// publishes a "request.approved" event on ChannelRequests with the correct
// request_id and user_id in the payload.
func TestApprove_PublishesRequestApproved(t *testing.T) {
	store := newFakeStore()
	store.requests["req-1"] = &Request{
		ID:                   "req-1",
		MediaType:            MediaTypeMovie,
		TMDBID:               550,
		Title:                "Fight Club",
		Status:               StatusPending,
		Outcome:              OutcomeActive,
		RequestedByUserID:    42,
		RequestedByProfileID: "profile-42",
	}
	// No router configured so submitApprovedRequest marks request failed —
	// which is still "after" approve; we verify the event fires regardless.
	service := newTestService(store)
	hub := evt.NewHub("test", nil)
	service.SetEventsHub(hub)

	ch, unsub := hub.Subscribe()
	defer unsub()

	_, _ = service.Approve(context.Background(), Viewer{UserID: 99, IsAdmin: true}, "req-1")

	select {
	case env := <-ch:
		if env.Channel != evt.ChannelRequests {
			t.Fatalf("channel = %q, want %q", env.Channel, evt.ChannelRequests)
		}
		if env.Event != "request.approved" {
			t.Fatalf("event = %q, want request.approved", env.Event)
		}
		var payload RequestEventPayload
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload.RequestID != "req-1" {
			t.Fatalf("request_id = %q, want req-1", payload.RequestID)
		}
		if payload.UserID != 42 {
			t.Fatalf("user_id = %d, want 42", payload.UserID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request.approved event")
	}
}

// TestPublish_NilHubIsNoOp verifies that operating without SetEventsHub does
// not panic and the Approve call succeeds normally.
func TestPublish_NilHubIsNoOp(t *testing.T) {
	store := newFakeStore()
	store.requests["req-2"] = &Request{
		ID:                "req-2",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Title:             "Fight Club",
		Status:            StatusPending,
		Outcome:           OutcomeActive,
		RequestedByUserID: 7,
	}
	service := newTestService(store)
	// No hub attached — must not panic.
	_, _ = service.Approve(context.Background(), Viewer{UserID: 99, IsAdmin: true}, "req-2")
}

func TestSubmitApprovedPopulatesRequesterIdentity(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInstOn("router-1", 1)}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)
	service.SetRequesterIdentityResolver(&fakeRequesterIdentity{email: "u@example.com", username: "bob"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := service.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.gotRequesterEmail != "u@example.com" || router.gotRequesterUsername != "bob" {
		t.Fatalf("descriptor identity = %q/%q, want u@example.com/bob", router.gotRequesterEmail, router.gotRequesterUsername)
	}
}

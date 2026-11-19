package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCreateRequestGroupPolicyCanForbidRequests(t *testing.T) {
	store := newFakeStore()
	service := newTestService(store)
	service.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: false}})

	_, err := service.CreateRequest(context.Background(), testViewer(1), fightClubInput)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created requests = %d, want 0", len(store.created))
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

	_, err := service.CreateRequest(context.Background(), testViewer(1), fightClubInput)
	if !errors.Is(err, ErrAlreadyRequested) {
		t.Fatalf("error = %v, want ErrAlreadyRequested", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created requests = %d, want 0", len(store.created))
	}
}

func TestCreateRequestAutoApprovalSubmitsMovie(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	// The repo decrypts api_key_ref on read, so the connection carries the literal
	// key here (no host-side secret resolution).
	store.integrations = []Integration{autoApproveRouterInst("router-1", "radarr-key")}
	service, router := newRoutedService(store)

	req, err := service.CreateRequest(context.Background(), testViewer(1), fightClubInput)
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

type fakeTVDBResolver struct {
	tvdbID    int
	err       error
	gotTMDBID int
	gotIMDbID string
	calls     int
}

func (f *fakeTVDBResolver) ResolveSeriesTVDBID(_ context.Context, tmdbID int, imdbID string) (int, error) {
	f.calls++
	f.gotTMDBID = tmdbID
	f.gotIMDbID = imdbID
	return f.tvdbID, f.err
}

func TestCreateRequestDoesNotTrustCallerIMDbIDForTVDBLookup(t *testing.T) {
	store := newFakeStore()
	// TMDB reports no IMDb ID, so the caller's cannot be corroborated.
	service := newTestServiceWithTMDB(store, &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{}})
	resolver := &fakeTVDBResolver{tvdbID: 456789}
	service.SetTVDBIDResolver(resolver)

	if _, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries, TMDBID: 240001, IMDbID: "tt0000001", Title: "Regional Series",
	}); err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if resolver.gotIMDbID != "" || resolver.gotTMDBID != 240001 {
		t.Fatalf("resolver got tmdb=%d imdb=%q, want the TMDB id only", resolver.gotTMDBID, resolver.gotIMDbID)
	}
	if store.created[0].Input.IMDbID != "tt0000001" {
		t.Fatalf("stored imdb_id = %q, want the caller's value kept", store.created[0].Input.IMDbID)
	}
}

// refreshingTMDBClient layers RefreshExternalIDs onto fakeTMDBClient and
// answers it with fresh data, standing in for an ID just added on TMDB.
type refreshingTMDBClient struct {
	*fakeTMDBClient
	fresh        *tmdb.ExternalIDs
	refreshErr   error
	refreshCalls int
}

func (c *refreshingTMDBClient) RefreshExternalIDs(context.Context, string, int) (*tmdb.ExternalIDs, error) {
	c.refreshCalls++
	if c.refreshErr != nil {
		return nil, c.refreshErr
	}
	return c.fresh, nil
}

func TestRetryKeepsFailedTargetWhenResolvedTVDBIDCannotBeSaved(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.setExternalIDsErr = errors.New("db unavailable")
	store.requests["req-1"] = &Request{
		ID: "req-1", MediaType: MediaTypeSeries, TMDBID: 240001,
		Status: StatusQueued, Outcome: OutcomeFailed,
	}
	store.targets = map[string][]Target{"req-1": {{
		ID: 7, RequestID: "req-1", Quality: Quality1080p, Status: StatusFailed, LastError: "sonarr: tvdb_id is required",
	}}}
	service := newTestService(store)
	router := &fakeRouterProvider{}
	service.SetRouterProvider(router)
	service.SetTVDBIDResolver(&fakeTVDBResolver{tvdbID: 456789})

	// The reopened approval stands; the save error defers the submission.
	got, err := service.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1")
	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if !strings.Contains(got.LastError, "db unavailable") {
		t.Fatalf("last_error = %q, want the save error", got.LastError)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want none after failed ID persistence", router.fulfillCalls)
	}
	if got := store.targets["req-1"]; len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("targets = %+v, want the failed target kept", got)
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
	service.SetUserRepository(requestUserRepo{})

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
		{"discover rows", func() error {
			_, err := service.DiscoverRows(context.Background(), viewer)
			return err
		}},
		{"discover row", func() error {
			_, err := service.DiscoverRow(context.Background(), viewer, "popular_movies", 1)
			return err
		}},
		{"detail", func() error {
			_, err := service.GetDetail(context.Background(), viewer, MediaTypeMovie, 550)
			return err
		}},
		{"create", func() error {
			_, err := service.CreateRequest(context.Background(), viewer, fightClubInput)
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
		{"remove failed", func() error {
			return service.RemoveFailed(context.Background(), viewer, "req-1")
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

// seedStalledPresenceRequest builds a presence-available request carrying one
// live target whose last status transition was `age` ago.
func seedStalledPresenceRequest(t *testing.T, status Status, age time.Duration) (*fakeStore, *Service, time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	req := &Request{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeActive}
	store.candidates = []*Request{req}
	store.requests["req-1"] = req
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: "req-1", IntegrationID: "router-1", IntegrationKind: "radarr",
		Quality: Quality1080p, Status: status, ExternalID: "123", ExternalStatus: "queued",
		UpdatedAt: now.Add(-age),
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	service := NewService(store, &fakeTMDBClient{}, &fakePresence{available: map[MediaType]map[int]bool{
		MediaTypeMovie: {550: true},
	}})
	service.Now = func() time.Time { return now }
	return store, service, now
}

// Presence completes a request only through a target its router has stalled
// on. A router that never reports completion would otherwise pin the request
// open forever, because the presence shortcut stays disabled while a target
// is live. Inside the horizon the router still owns the target: presence must
// not short-circuit a submission that may simply be young. A downloading
// target gets a longer horizon (see
// TestReconcileRequestsPresenceRetiresStalledDownloadingTargets).
func TestReconcileRequestsPresenceRetiresOnlyStalledTargets(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        Status
		age           time.Duration
		wantCompleted int
		wantStatus    Status
	}{
		{"stalled queued", StatusQueued, 48 * time.Hour, 1, StatusCompleted},
		{"recent queued", StatusQueued, time.Hour, 0, StatusQueued},
		{"stalled downloading", StatusDownloading, 30 * 24 * time.Hour, 1, StatusCompleted},
		{"recent downloading", StatusDownloading, 48 * time.Hour, 0, StatusDownloading},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, service, _ := seedStalledPresenceRequest(t, tc.status, tc.age)

			result, err := service.ReconcileRequests(context.Background(), 100)
			if err != nil {
				t.Fatalf("ReconcileRequests returned error: %v", err)
			}
			if result.Completed != tc.wantCompleted {
				t.Fatalf("result = %+v, want %d completed", result, tc.wantCompleted)
			}
			targets, _ := store.ListTargets(context.Background(), "req-1")
			if len(targets) != 1 || targets[0].Status != tc.wantStatus {
				t.Fatalf("targets = %+v, want one %s target", targets, tc.wantStatus)
			}
			if tc.wantCompleted == 0 {
				return
			}
			if targets[0].ExternalStatus != ExternalStatusPresenceConfirmed {
				t.Fatalf("external status = %q, want %s", targets[0].ExternalStatus, ExternalStatusPresenceConfirmed)
			}
			if store.requests["req-1"].Status != StatusCompleted {
				t.Fatalf("request status = %q, want completed", store.requests["req-1"].Status)
			}
		})
	}
}

// A router can report downloading for good: Seerr leaves a movie in
// PROCESSING when its own availability check never runs, even once the file
// is imported (#3478). Presence closes out such a target, but a downloading
// target may also be a real download, and presence only knows the title is in
// the library: not at which quality, and for a whole series, not which
// episodes. So a target with download progress, a whole-series request, a
// season request whose seasons are not all complete, and a request that asked
// for another quality as well all keep their downloading target. A failed
// target fetched nothing, so it does not hold the other tier open; the request
// then aggregates to failed, as when its router reports the completion, so
// Retry can resubmit the failed tier. Neither does a target this backstop
// completed, since presence proved no file at its quality: otherwise a 2160p
// target retired while queued would pin a stuck 1080p one forever.
func TestReconcileRequestsPresenceRetiresStalledDownloadingTargets(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-30 * 24 * time.Hour)
	const seriesID = 1399
	movie := Request{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550}
	wholeSeries := Request{ID: "req-1", MediaType: MediaTypeSeries, TMDBID: seriesID}
	seasons := Request{ID: "req-1", MediaType: MediaTypeSeries, TMDBID: seriesID, Seasons: []int{1, 2}}
	complete := map[int]SeasonCounts{1: {Aired: 8, Have: 8}, 2: {Aired: 10, Have: 10}}
	partial := map[int]SeasonCounts{1: {Aired: 8, Have: 8}, 2: {Aired: 10, Have: 4}}
	downloading := Target{Quality: Quality1080p, Status: StatusDownloading, ExternalStatus: "3", UpdatedAt: stale}
	withProgress := downloading
	withProgress.Download = &DownloadProgress{Phase: DownloadPhaseStalled, BytesTotal: 100, BytesLeft: 60, UpdatedAt: now.Add(-time.Minute)}
	uhd := downloading
	uhd.Quality = Quality2160p
	hdDone := Target{Quality: Quality1080p, Status: StatusCompleted, ExternalStatus: "5", UpdatedAt: stale}
	uhdFailed := Target{Quality: Quality2160p, Status: StatusFailed, LastError: msgNoTargetForQuality, UpdatedAt: stale}
	uhdByPresence := Target{Quality: Quality2160p, Status: StatusCompleted, ExternalStatus: ExternalStatusPresenceConfirmed, UpdatedAt: stale}

	for _, tc := range []struct {
		name    string
		req     Request
		seasons map[int]SeasonCounts
		targets []Target
		// retired is whether the last target is completed by presence, and
		// status and outcome are the request's aggregate afterwards.
		retired bool
		status  Status
		outcome Outcome
	}{
		{"movie", movie, nil, []Target{downloading}, true, StatusCompleted, OutcomeActive},
		{"movie with download progress", movie, nil, []Target{withProgress}, false, StatusDownloading, OutcomeActive},
		{"2160p beside a completed 1080p target", movie, nil, []Target{hdDone, uhd}, false, StatusDownloading, OutcomeActive},
		{"2160p alone", movie, nil, []Target{uhd}, true, StatusCompleted, OutcomeActive},
		{"1080p beside a failed 2160p target", movie, nil, []Target{uhdFailed, downloading}, true, StatusQueued, OutcomeFailed},
		{"1080p beside a 2160p target completed by presence", movie, nil, []Target{uhdByPresence, downloading}, true, StatusCompleted, OutcomeActive},
		{"whole series", wholeSeries, nil, []Target{downloading}, false, StatusDownloading, OutcomeActive},
		{"seasons all complete", seasons, complete, []Target{downloading}, true, StatusCompleted, OutcomeActive},
		{"a season incomplete", seasons, partial, []Target{downloading}, false, StatusDownloading, OutcomeActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			req := tc.req
			req.Status, req.Outcome = StatusDownloading, OutcomeActive
			store.candidates = []*Request{&req}
			store.requests[req.ID] = &req
			for _, target := range tc.targets {
				target.RequestID = req.ID
				target.IntegrationID, target.IntegrationKind, target.ExternalID = "router-1", "seerr", "123"
				if _, err := store.CreateTarget(context.Background(), target); err != nil {
					t.Fatalf("seed target: %v", err)
				}
			}
			presence := &fakePresence{available: map[MediaType]map[int]bool{req.MediaType: {req.TMDBID: true}}}
			if tc.seasons != nil {
				presence.seasons = map[string]map[int]SeasonCounts{fakePresenceContentID(req.MediaType, req.TMDBID): tc.seasons}
			}
			service := NewService(store, &fakeTMDBClient{}, presence)
			service.Now = func() time.Time { return now }

			result, err := service.ReconcileRequests(context.Background(), 100)
			if err != nil {
				t.Fatalf("ReconcileRequests returned error: %v", err)
			}
			targets, _ := store.ListTargets(context.Background(), req.ID)
			last := targets[len(targets)-1]
			switch {
			case !tc.retired && last.Status != StatusDownloading:
				t.Fatalf("target = %+v, want it left downloading", last)
			case tc.retired && (last.Status != StatusCompleted || last.ExternalStatus != ExternalStatusPresenceConfirmed):
				t.Fatalf("target = %+v, want it completed as %s", last, ExternalStatusPresenceConfirmed)
			}
			got := store.requests[req.ID]
			if got.Status != tc.status || got.Outcome != tc.outcome {
				t.Fatalf("request = %s/%s, want %s/%s", got.Status, got.Outcome, tc.status, tc.outcome)
			}
			if wantCompleted := tc.status == StatusCompleted; (result.Completed == 1) != wantCompleted {
				t.Fatalf("result = %+v, want completed %v", result, wantCompleted)
			}
		})
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
	service, router := newRoutedService(store)

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
	service, router := newRoutedService(store)

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
	service, router := newRoutedService(store)

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
	service, router := newRoutedService(store)

	_, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:      "router-1",
		BaseURL: "http://attacker.example",
	})
	// The stored key stays with the stored address: a changed URL needs the
	// key typed again, and the plugin is never asked without one.
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.FieldErrors["api_key_ref"] != integrationKeyMissing {
		t.Fatalf("err = %v, want the api_key_ref field error", err)
	}
	if router.gotOptionsConn.APIKey != "" || router.gotOptionsConn.BaseURL != "" {
		t.Fatalf("probe conn = %+v, want no probe for changed base URL without a key", router.gotOptionsConn)
	}
}

// The owner and an admin may close a pending request; anyone else may not.
func TestCancelPendingRequestAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, id, reason string
		viewer           Viewer
		wantErr          error
		wantCancelled    bool
	}{
		{"owner", "req-mine", "no longer want", Viewer{UserID: 7, ProfileID: "profile-1"}, nil, true},
		{"non-owner", "req-someone-else", "", Viewer{UserID: 8, ProfileID: "profile-2"}, ErrForbidden, false},
		{"admin", "req-other", "house cleaning", Viewer{UserID: 99, IsAdmin: true}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.requests[tc.id] = &Request{
				ID:                tc.id,
				MediaType:         MediaTypeMovie,
				TMDBID:            550,
				Status:            StatusPending,
				Outcome:           OutcomeActive,
				RequestedByUserID: 7,
			}
			req, err := newTestService(store).Cancel(context.Background(), tc.viewer, tc.id, tc.reason)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantCancelled && req.Outcome != OutcomeCancelled {
				t.Fatalf("Outcome = %q, want canceled", req.Outcome)
			}
		})
	}
}

func TestRemoveFailedRequestAuthority(t *testing.T) {
	for _, tc := range []struct {
		name        string
		viewer      Viewer
		wantErr     error
		wantRemoved bool
	}{
		{"owner", Viewer{UserID: 7, ProfileID: "profile-1"}, nil, true},
		{"non-owner", Viewer{UserID: 8, ProfileID: "profile-2"}, ErrForbidden, false},
		{"admin", Viewer{UserID: 99, IsAdmin: true}, nil, true},
		{"no account", Viewer{}, ErrForbidden, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.requests["req-failed"] = &Request{
				ID:                "req-failed",
				MediaType:         MediaTypeMovie,
				TMDBID:            550,
				Status:            StatusApproved,
				Outcome:           OutcomeFailed,
				RequestedByUserID: 7,
			}
			err := newTestService(store).RemoveFailed(context.Background(), tc.viewer, "req-failed")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if _, kept := store.requests["req-failed"]; kept == tc.wantRemoved {
				t.Fatalf("request kept = %v, want removed = %v", kept, tc.wantRemoved)
			}
		})
	}
}

func TestCancelRejectsRequestsAlreadyInFulfillment(t *testing.T) {
	inFlight := time.Now().Add(time.Minute)
	cases := []struct {
		name string
		req  Request
	}{
		{"approved and being submitted", Request{Status: StatusApproved, Outcome: OutcomeActive, SubmitLeaseUntil: &inFlight}},
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

func newTestService(store *fakeStore) *Service {
	return newTestServiceWithTMDB(store, &fakeTMDBClient{})
}

func newTestServiceWithTMDB(store *fakeStore, tmdbClient *fakeTMDBClient) *Service {
	service := NewService(store, tmdbClient, &fakePresence{})
	service.Now = func() time.Time { return time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC) }
	service.SetUserRepository(requestUserRepo{})
	return service
}

// newRoutedService is newTestService with a canned router provider.
func newRoutedService(store *fakeStore) (*Service, *fakeRouterProvider) {
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)
	return service, router
}

// fightClubInput is a movie request for TMDB 550.
var fightClubInput = CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: 550, Title: "Fight Club"}

// submitApprovedMovie stores approved movie request r1 from user 7 and submits
// it as an admin.
func submitApprovedMovie(service *Service, store *fakeStore) (*Request, error) {
	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	return service.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
}

// mustSubmitApprovedMovie is submitApprovedMovie, failing the test on an error.
func mustSubmitApprovedMovie(t *testing.T, service *Service, store *fakeStore) {
	t.Helper()
	if _, err := submitApprovedMovie(service, store); err != nil {
		t.Fatalf("submit: %v", err)
	}
}

// requestUserRepo stands in for the account loader. The default account is
// bound to an access group and sets no overrides, so the group policy decides.
type requestUserRepo struct {
	user *models.User
	err  error
}

func (r requestUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.user != nil {
		return r.user, nil
	}
	groupID := int64(1)
	return &models.User{ID: id, AccessGroupID: &groupID}, nil
}

func testViewer(userID int) Viewer {
	return Viewer{UserID: userID, ProfileID: "profile-1"}
}

type fakeStore struct {
	mu            sync.Mutex
	adminFilters  []ListFilter
	viewCounts    AdminViewCounts
	events        map[string][]RequestEvent
	settings      Settings
	limit         *UserLimit
	count         int
	active        map[MediaType]map[int]*Request
	created       []CreateRequestRecord
	integrations  []Integration
	candidates    []*Request
	waiting       []*Request
	mine          []*Request
	statusUpdates []Status
	requests      map[string]*Request
	targets       map[string][]Target
	targetSeq     int64
	unnotified    []string
	notified      []string
	reconciled    []string
	follows       map[string]Follower // key: media_type/tmdb_id/user_id/profile_id
	followFor     map[string]string   // follow key -> the request it waits for
	clearErr      error               // returned by ClearRequestFollowers when set
	markErr       error               // returned by MarkFulfilledNotified when set
	routes        []Route
	factsSet      map[string]RoutingFacts
	groupLimits   map[int64]*GroupLimit
	// userLimitReads counts policy resolutions (each reads the account's limit once).
	userLimitReads int
	// trackActive makes CreateRequest record the new request as the title's
	// open one, the way ListActiveByTMDB reads the repository.
	trackActive bool

	setExternalIDsErr error

	// downloadWrites records each progress write UpdateTargetDownload
	// applied; downloadErr fails it. downloadChecked holds when each target
	// was last asked about, as the repository's download_checked_at, and
	// downloadChecks the targets MarkTargetDownloadChecked stamped.
	downloadWrites  []downloadWrite
	downloadErr     error
	downloadChecked map[int64]time.Time
	downloadChecks  []int64
	// externalStatusWrites records the targets UpdateTargetExternalStatus
	// wrote.
	externalStatusWrites []int64

	listIntegrationsCalls int
	getSettingsCalls      int
}

type downloadWrite struct {
	targetID int64
	progress *DownloadProgress
}

type requestGroupProvider struct {
	group *access.GroupPolicy
	err   error
}

func (p requestGroupProvider) GetPolicyForUser(context.Context, int) (*access.GroupPolicy, error) {
	return p.group, p.err
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
	f.userLimitReads++
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
	return f.usedLocked(userID, since), nil
}

// usedLocked counts the user's stored requests created since the window start,
// on top of the count baseline, the way the repository counts rows. A deleted
// row stops counting. Callers hold f.mu.
func (f *fakeStore) usedLocked(userID int, since time.Time) int {
	used := f.count
	for _, req := range f.requests {
		if req.RequestedByUserID == userID && !req.CreatedAt.Before(since) {
			used++
		}
	}
	return used
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

func (f *fakeStore) ListRequestedTMDB(_ context.Context, viewer Viewer, mediaType MediaType, ids []int) (map[int]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]bool{}
	for _, req := range f.requests {
		if req.MediaType == mediaType && slices.Contains(ids, req.TMDBID) && req.requestedBy(viewer) {
			out[req.TMDBID] = true
		}
	}
	return out, nil
}

func (f *fakeStore) ListProfileWatchlistRequests(_ context.Context, userID int, profileID string) ([]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Request
	for _, req := range f.requests {
		if req.RequestedByUserID == userID && req.RequestedByProfileID == profileID &&
			req.Source == SourceWatchlist && req.Outcome == OutcomeActive && req.Status != StatusCompleted {
			out = append(out, req)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateRequest(_ context.Context, input CreateRequestRecord) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if input.ReplaceFailed {
		for id, req := range f.requests {
			if req.RequestedByUserID == input.Requester.UserID && req.MediaType == input.Input.MediaType &&
				req.TMDBID == input.Input.TMDBID && req.Outcome == OutcomeFailed {
				delete(f.requests, id)
			}
		}
	}
	if input.Quota != nil && f.usedLocked(input.Quota.UserID, input.Quota.WindowStart) >= input.Quota.MaxRequests {
		return nil, ErrQuotaExceeded
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
		RoutingFacts:         input.Facts,
		Seasons:              input.Input.Seasons,
		RequestedByUserID:    input.Requester.UserID,
		RequestedByProfileID: input.Requester.ProfileID,
		Source:               requestSource(input.Input.Source),
		CreatedAt:            input.Now,
		UpdatedAt:            input.Now,
	}
	f.requests[input.ID] = req
	if f.trackActive && req.Outcome == OutcomeActive {
		f.active[req.MediaType][req.TMDBID] = req
	}
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

func (f *fakeStore) ListLibraryWaitCandidates(context.Context, int) ([]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waiting, nil
}

// ListDownloadingRequests mirrors the repository: active requests with a
// downloading target that has progress, by the one asked about longest ago,
// then id. A seeded target that was never asked about counts as asked when
// its progress was reported.
func (f *fakeStore) ListDownloadingRequests(_ context.Context, limit int) ([]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	type candidate struct {
		req     *Request
		checked time.Time
	}
	var found []candidate
	for id, targets := range f.targets {
		req := f.requests[id]
		if req == nil || req.Outcome != OutcomeActive {
			continue
		}
		var checked *time.Time
		for _, t := range targets {
			if t.Status != StatusDownloading || t.Download == nil {
				continue
			}
			at, ok := f.downloadChecked[t.ID]
			if !ok {
				at = t.Download.UpdatedAt
			}
			if checked == nil || at.Before(*checked) {
				checked = &at
			}
		}
		if checked != nil {
			copy := *req
			found = append(found, candidate{req: &copy, checked: *checked})
		}
	}
	slices.SortFunc(found, func(a, b candidate) int {
		if c := a.checked.Compare(b.checked); c != 0 {
			return c
		}
		return strings.Compare(a.req.ID, b.req.ID)
	})
	out := make([]*Request, 0, len(found))
	for _, c := range found {
		if limit > 0 && len(out) == limit {
			break
		}
		out = append(out, c.req)
	}
	return out, nil
}

func (f *fakeStore) ListFulfilledUnnotified(context.Context, int) ([]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*Request, 0, len(f.unnotified))
	for _, id := range f.unnotified {
		if req := f.requests[id]; req != nil {
			copy := *req
			out = append(out, &copy)
		}
	}
	return out, nil
}

func (f *fakeStore) SetExternalIDs(_ context.Context, id string, tvdbID int, imdbID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setExternalIDsErr != nil {
		return 0, f.setExternalIDsErr
	}
	req := f.requests[id]
	if req == nil {
		return 0, ErrNotFound
	}
	if req.TVDBID == nil || *req.TVDBID <= 0 {
		v := tvdbID
		req.TVDBID = &v
	}
	if req.IMDbID == "" {
		req.IMDbID = imdbID
	}
	return *req.TVDBID, nil
}

func (f *fakeStore) MarkFulfilledNotified(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return false, f.markErr
	}
	if slices.Contains(f.notified, id) {
		return false, nil
	}
	kept := f.unnotified[:0]
	for _, pending := range f.unnotified {
		if pending != id {
			kept = append(kept, pending)
		}
	}
	f.unnotified = kept
	f.notified = append(f.notified, id)
	return true, nil
}

func (f *fakeStore) ListMine(_ context.Context, _ int, filter ListFilter) ([]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Request
	for _, req := range f.mine {
		if filter.RequestedByProfileID == "" || req.RequestedByProfileID == filter.RequestedByProfileID {
			out = append(out, req)
		}
	}
	return out, nil
}

func (f *fakeStore) ListAdmin(_ context.Context, filter ListFilter) ([]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adminFilters = append(f.adminFilters, filter)
	return nil, nil
}

func (f *fakeStore) GetGroupLimit(_ context.Context, groupID int64) (*GroupLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit := f.groupLimits[groupID]; limit != nil {
		out := *limit
		return &out, nil
	}
	return nil, nil
}

func (f *fakeStore) GroupExists(_ context.Context, groupID int64) (bool, error) {
	return groupID == 1, nil
}

func (f *fakeStore) UpsertGroupLimitConditional(_ context.Context, in GroupLimit, expected int64) (*GroupLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.groupLimits[in.GroupID]
	var revision int64
	if current != nil {
		revision = current.Revision
	}
	if expected != -1 && expected != revision {
		return nil, ErrStaleRevision
	}
	if f.groupLimits == nil {
		f.groupLimits = map[int64]*GroupLimit{}
	}
	in.Revision = revision + 1
	f.groupLimits[in.GroupID] = &in
	out := in
	return &out, nil
}

func (f *fakeStore) CountAdminViews(context.Context) (AdminViewCounts, error) {
	return f.viewCounts, nil
}

func (f *fakeStore) ListEvents(_ context.Context, requestID string, _ int) ([]RequestEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RequestEvent(nil), f.events[requestID]...), nil
}

// guardAccepts mirrors the repository's guarded UPDATE. Callers hold f.mu.
func (f *fakeStore) guardAccepts(g StateGuard, req *Request) bool {
	statusOK := len(g.Statuses) == 0 || slices.Contains(g.Statuses, req.Status)
	outcomeOK := len(g.Outcomes) == 0 || slices.Contains(g.Outcomes, req.Outcome)
	if !statusOK || !outcomeOK {
		return false
	}
	if g.UnsentOnly && req.Status == StatusApproved {
		leased := req.SubmitLeaseUntil != nil && req.SubmitLeaseUntil.After(time.Now())
		sent := slices.ContainsFunc(f.targets[req.ID], func(t Target) bool { return t.Status != StatusFailed })
		return !leased && !sent
	}
	return true
}

// lookupLocked finds a request by id, falling back to the reconcile and
// library-wait candidates so tests that only seed those still resolve.
// Callers hold f.mu.
func (f *fakeStore) lookupLocked(id string) *Request {
	if req := f.requests[id]; req != nil {
		return req
	}
	for _, c := range append(append([]*Request(nil), f.candidates...), f.waiting...) {
		if c != nil && c.ID == id {
			copy := *c
			f.requests[id] = &copy
			return &copy
		}
	}
	return nil
}

func (f *fakeStore) SetStatus(_ context.Context, id string, from StateGuard, status Status, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		req = &Request{ID: id, Outcome: OutcomeActive}
		f.requests[id] = req
	} else if !f.guardAccepts(from, req) {
		return nil, ErrInvalidState
	}
	f.statusUpdates = append(f.statusUpdates, status)
	req.Status = status
	if status == StatusApproved {
		req.SubmitAttempts = 0
		req.SubmitLeaseUntil = nil
		req.NextSubmitAt = nil
	}
	copy := *req
	return &copy, nil
}

func (f *fakeStore) SetOutcome(_ context.Context, id string, from StateGuard, outcome Outcome, _ Viewer, message string) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		req = &Request{ID: id}
		f.requests[id] = req
	} else if !f.guardAccepts(from, req) {
		return nil, ErrInvalidState
	}
	req.Outcome = outcome
	req.LastError = message
	if outcome == OutcomeDeclined || outcome == OutcomeCancelled {
		req.OutcomeReason = message
		for key := range f.follows {
			if f.followFor[key] == req.ID {
				delete(f.follows, key)
			}
		}
	}
	copy := *req
	return &copy, nil
}

func (f *fakeStore) DeleteFailed(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return ErrNotFound
	}
	if req.Outcome != OutcomeFailed {
		return ErrInvalidState
	}
	delete(f.requests, req.ID)
	return nil
}

func (f *fakeStore) ReopenFailed(_ context.Context, id string, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	if req.Outcome != OutcomeFailed {
		return nil, ErrInvalidState
	}
	req.Outcome = OutcomeActive
	req.Status = StatusApproved
	req.LastError = ""
	req.SubmitAttempts = 0
	req.SubmitLeaseUntil = nil
	req.NextSubmitAt = nil
	copy := *req
	return &copy, nil
}

func (f *fakeStore) ClaimSubmission(_ context.Context, id string, lease time.Duration) (*Request, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil || req.Status != StatusApproved || req.Outcome != OutcomeActive {
		return nil, false, nil
	}
	now := time.Now()
	if (req.SubmitLeaseUntil != nil && req.SubmitLeaseUntil.After(now)) || (req.NextSubmitAt != nil && req.NextSubmitAt.After(now)) {
		return nil, false, nil
	}
	req.SubmitAttempts++
	until := now.Add(lease)
	req.SubmitLeaseUntil = &until
	copy := *req
	return &copy, true, nil
}

func (f *fakeStore) DeferSubmission(_ context.Context, id string, leaseUntil time.Time, delay time.Duration, message string) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive ||
		req.SubmitLeaseUntil == nil || !req.SubmitLeaseUntil.Equal(leaseUntil) {
		return nil, ErrInvalidState
	}
	next := time.Now().Add(delay)
	req.NextSubmitAt = &next
	req.SubmitLeaseUntil = nil
	req.LastError = message
	copy := *req
	return &copy, nil
}

func (f *fakeStore) FailSubmission(_ context.Context, id string, leaseUntil time.Time, _ Viewer, message string) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive ||
		req.SubmitLeaseUntil == nil || !req.SubmitLeaseUntil.Equal(leaseUntil) {
		return nil, ErrInvalidState
	}
	req.Outcome = OutcomeFailed
	req.SubmitLeaseUntil = nil
	req.LastError = message
	copy := *req
	return &copy, nil
}

func (f *fakeStore) MarkAvailable(_ context.Context, id string, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	open := req.Status == StatusPending || req.Status == StatusApproved || req.Status == StatusQueued || req.Status == StatusDownloading
	partlyDelivered := false
	for _, t := range f.targets[id] {
		if t.Status == StatusCompleted {
			partlyDelivered = true
		}
	}
	// An approved request that delivered a quality is resending the rest.
	submitting := req.Status == StatusApproved &&
		(partlyDelivered || req.SubmitLeaseUntil != nil && req.SubmitLeaseUntil.After(time.Now()))
	failedElsewhere := req.Outcome == OutcomeFailed && !partlyDelivered
	if !failedElsewhere && (req.Outcome != OutcomeActive || !open || submitting) {
		return nil, ErrInvalidState
	}
	f.statusUpdates = append(f.statusUpdates, StatusCompleted)
	req.Status = StatusCompleted
	req.Outcome = OutcomeActive
	req.LastError = ""
	copy := *req
	return &copy, nil
}

func (f *fakeStore) MarkReconciled(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconciled = append(f.reconciled, id)
	return nil
}

func (f *fakeStore) RecomputeStatus(_ context.Context, id string, leaseUntil time.Time, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive || !f.holdsLeaseLocked(req, leaseUntil) {
		return nil, ErrInvalidState
	}
	req.Status, req.Outcome = aggregateStatus(f.targets[id])
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

// followKey names one profile's follow of one request of a title.
func followKey(mediaType MediaType, tmdbID int, userID int, profileID, requestID string) string {
	return fmt.Sprintf("%s/%d/%d/%s/%s", mediaType, tmdbID, userID, profileID, requestID)
}

func (f *fakeStore) FollowTitle(_ context.Context, mediaType MediaType, tmdbID int, viewer Viewer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req := f.active[mediaType][tmdbID]; req == nil || req.Outcome != OutcomeActive || req.Status == StatusCompleted {
		return ErrNotRequested
	}
	f.seedFollowLocked(mediaType, tmdbID, viewer)
	return nil
}

// seedFollow records a follow directly, as one added before the request
// completed. Tests use it for titles whose request is already closed.
func (f *fakeStore) seedFollow(mediaType MediaType, tmdbID int, viewer Viewer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seedFollowLocked(mediaType, tmdbID, viewer)
}

// seedFollowLocked records a follow for the title's open request, or else
// for the title's request in f.requests with the lowest id.
func (f *fakeStore) seedFollowLocked(mediaType MediaType, tmdbID int, viewer Viewer) {
	requestID := ""
	if req := f.active[mediaType][tmdbID]; req != nil {
		requestID = req.ID
	} else {
		for id, req := range f.requests {
			if req.MediaType == mediaType && req.TMDBID == tmdbID && (requestID == "" || id < requestID) {
				requestID = id
			}
		}
	}
	f.seedFollowForLocked(mediaType, tmdbID, viewer, requestID)
}

func (f *fakeStore) seedFollowForLocked(mediaType MediaType, tmdbID int, viewer Viewer, requestID string) {
	if f.follows == nil {
		f.follows = map[string]Follower{}
	}
	if f.followFor == nil {
		f.followFor = map[string]string{}
	}
	key := followKey(mediaType, tmdbID, viewer.UserID, viewer.ProfileID, requestID)
	f.follows[key] = Follower{UserID: viewer.UserID, ProfileID: viewer.ProfileID}
	f.followFor[key] = requestID
}

func (f *fakeStore) UnfollowTitle(_ context.Context, mediaType MediaType, tmdbID int, viewer Viewer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := fmt.Sprintf("%s/%d/%d/%s/", mediaType, tmdbID, viewer.UserID, viewer.ProfileID)
	for key := range f.follows {
		if strings.HasPrefix(key, prefix) {
			delete(f.follows, key)
		}
	}
	return nil
}

func (f *fakeStore) FollowedRequests(_ context.Context, requestIDs []string, viewer Viewer) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for key, follower := range f.follows {
		if follower.UserID == viewer.UserID && follower.ProfileID == viewer.ProfileID && slices.Contains(requestIDs, f.followFor[key]) {
			out[f.followFor[key]] = true
		}
	}
	return out, nil
}

func (f *fakeStore) ListRequestFollowers(_ context.Context, req Request) ([]Follower, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Follower
	for key, follower := range f.follows {
		if f.followFor[key] == req.ID {
			out = append(out, follower)
		}
	}
	slices.SortFunc(out, func(a, b Follower) int {
		if c := strings.Compare(a.ProfileID, b.ProfileID); c != 0 {
			return c
		}
		return a.UserID - b.UserID
	})
	return out, nil
}

// titleFollowers lists every follow on a title, whichever request it waits for.
func (f *fakeStore) titleFollowers(mediaType MediaType, tmdbID int) ([]Follower, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := fmt.Sprintf("%s/%d/", mediaType, tmdbID)
	var out []Follower
	for key, follower := range f.follows {
		if strings.HasPrefix(key, prefix) {
			out = append(out, follower)
		}
	}
	return out, nil
}

func (f *fakeStore) ClearRequestFollowers(_ context.Context, req Request, followers []Follower) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clearErr != nil {
		return f.clearErr
	}
	for _, follower := range followers {
		delete(f.follows, followKey(req.MediaType, req.TMDBID, follower.UserID, follower.ProfileID, req.ID))
	}
	return nil
}

func (f *fakeStore) ListRoutes(context.Context) ([]Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.routes), nil
}

func (f *fakeStore) SetRoutingFacts(_ context.Context, id string, facts RoutingFacts) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	if req.RoutingFacts.Captured() {
		out := *req
		return &out, nil
	}
	if f.factsSet == nil {
		f.factsSet = map[string]RoutingFacts{}
	}
	f.factsSet[id] = facts
	req.RoutingFacts = facts
	req.IsAnime = facts.Anime
	copy := *req
	return &copy, nil
}

func (f *fakeStore) UpdateRoutingFacts(_ context.Context, id string, facts RoutingFacts) (*Request, error) {
	return f.mergeRoutingFacts(id, facts, false)
}

func (f *fakeStore) FillRoutingFacts(_ context.Context, id string, facts RoutingFacts) (*Request, error) {
	return f.mergeRoutingFacts(id, facts, true)
}

// mergeRoutingFacts merges facts into the stored ones key by key, as the
// repository's jsonb concatenation does; keepStored keeps a key both hold.
func (f *fakeStore) mergeRoutingFacts(id string, facts RoutingFacts, keepStored bool) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	stored, patch := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for target, from := range map[*map[string]json.RawMessage]RoutingFacts{&stored: req.RoutingFacts, &patch: facts} {
		raw, err := json.Marshal(from)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return nil, err
		}
	}
	for key, value := range patch {
		if _, held := stored[key]; !held || !keepStored {
			stored[key] = value
		}
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	var merged RoutingFacts
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil, err
	}
	req.RoutingFacts = merged
	out := *req
	return &out, nil
}

func (f *fakeStore) ListTargets(_ context.Context, requestID string) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Target(nil), f.targets[requestID]...), nil
}

func (f *fakeStore) ListTargetsForRequests(_ context.Context, requestIDs []string) (map[string][]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]Target{}
	for _, id := range requestIDs {
		if targets := f.targets[id]; len(targets) > 0 {
			out[id] = append([]Target(nil), targets...)
		}
	}
	return out, nil
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

// holdsLeaseLocked reports whether req is claimed with leaseUntil. A request
// stored without a lease accepts the zero time, as a test that seeds an
// approved request and calls the planner directly passes it.
func (f *fakeStore) holdsLeaseLocked(req *Request, leaseUntil time.Time) bool {
	if req.SubmitLeaseUntil == nil {
		return leaseUntil.IsZero()
	}
	return req.SubmitLeaseUntil.Equal(leaseUntil)
}

func (f *fakeStore) DeleteTarget(_ context.Context, requestID string, id int64, leaseUntil time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(requestID)
	if req == nil || req.Status != StatusApproved || req.Outcome != OutcomeActive || !f.holdsLeaseLocked(req, leaseUntil) {
		return ErrInvalidState
	}
	ts := f.targets[requestID]
	for i := range ts {
		if ts[i].ID == id {
			f.targets[requestID] = append(ts[:i], ts[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// UpdateTargetStatus mirrors the repository: it writes only a queued or
// downloading target and answers ErrInvalidState for a finished one.
func (f *fakeStore) UpdateTargetStatus(_ context.Context, targetID int64, status Status, externalID, externalStatus, lastErr string, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ts := range f.targets {
		for _, t := range ts {
			if t.ID == targetID && t.Status != StatusQueued && t.Status != StatusDownloading {
				return nil, ErrInvalidState
			}
		}
	}
	return f.updateTargetLocked(targetID, status, externalID, externalStatus, lastErr)
}

// UpdateTargetDownload mirrors the repository: it writes only while the
// target is queued or downloading, and touches nothing but the progress.
// Every write it applies is recorded in downloadWrites.
func (f *fakeStore) UpdateTargetDownload(_ context.Context, targetID int64, progress *DownloadProgress) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downloadErr != nil {
		return f.downloadErr
	}
	for rid, ts := range f.targets {
		for i := range ts {
			if ts[i].ID != targetID {
				continue
			}
			if ts[i].Status != StatusQueued && ts[i].Status != StatusDownloading {
				return nil
			}
			var stored *DownloadProgress
			if f.downloadChecked == nil {
				f.downloadChecked = map[int64]time.Time{}
			}
			delete(f.downloadChecked, targetID)
			if progress != nil {
				copy := *progress
				copy.UpdatedAt = time.Now().UTC()
				stored = &copy
				f.downloadChecked[targetID] = copy.UpdatedAt
			}
			f.targets[rid][i].Download = stored
			f.downloadWrites = append(f.downloadWrites, downloadWrite{targetID: targetID, progress: stored})
			return nil
		}
	}
	return nil
}

// MarkTargetDownloadChecked mirrors the repository: it stamps a queued or
// downloading target that has progress as asked about now, leaving the
// progress alone, and records the stamp in downloadChecks.
func (f *fakeStore) MarkTargetDownloadChecked(_ context.Context, targetID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downloadErr != nil {
		return f.downloadErr
	}
	for _, ts := range f.targets {
		for _, t := range ts {
			if t.ID != targetID {
				continue
			}
			if t.Download == nil || (t.Status != StatusQueued && t.Status != StatusDownloading) {
				return nil
			}
			if f.downloadChecked == nil {
				f.downloadChecked = map[int64]time.Time{}
			}
			f.downloadChecked[targetID] = time.Now().UTC()
			f.downloadChecks = append(f.downloadChecks, targetID)
			return nil
		}
	}
	return nil
}

// UpdateTargetExternalStatus mirrors the repository: it writes only a changed
// raw status on a queued or downloading target, and touches nothing else.
func (f *fakeStore) UpdateTargetExternalStatus(_ context.Context, targetID int64, externalStatus string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for rid, ts := range f.targets {
		for i := range ts {
			if ts[i].ID != targetID {
				continue
			}
			if (ts[i].Status != StatusQueued && ts[i].Status != StatusDownloading) || ts[i].ExternalStatus == externalStatus {
				return nil
			}
			f.targets[rid][i].ExternalStatus = externalStatus
			f.externalStatusWrites = append(f.externalStatusWrites, targetID)
			return nil
		}
	}
	return nil
}

// RecordSubmission mirrors the repository's lease fence and its replacement of
// the failed targets of the qualities it records, then records each target the
// way a create followed by a status update would.
func (f *fakeStore) RecordSubmission(_ context.Context, id string, leaseUntil time.Time, targets []Target, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive ||
		req.SubmitLeaseUntil == nil || !req.SubmitLeaseUntil.Equal(leaseUntil) {
		return nil, ErrInvalidState
	}
	req.SubmitLeaseUntil = nil
	if f.targets == nil {
		f.targets = map[string][]Target{}
	}
	f.targets[id] = slices.DeleteFunc(f.targets[id], func(old Target) bool {
		return old.Status == StatusFailed && slices.ContainsFunc(targets, func(t Target) bool { return t.Quality == old.Quality })
	})
	latest := req
	for _, t := range targets {
		f.targetSeq++
		t.ID = f.targetSeq
		t.RequestID = id
		f.targets[id] = append(f.targets[id], t)
		updated, err := f.updateTargetLocked(t.ID, t.Status, t.ExternalID, t.ExternalStatus, t.LastError)
		if err != nil {
			return nil, err
		}
		latest = updated
	}
	copy := *latest
	return &copy, nil
}

// updateTargetLocked is UpdateTargetStatus for callers holding f.mu.
func (f *fakeStore) updateTargetLocked(targetID int64, status Status, externalID, externalStatus, lastErr string) (*Request, error) {
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
				if status == StatusCompleted || status == StatusFailed {
					f.targets[rid][i].Download = nil
					delete(f.downloadChecked, targetID)
				}
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
	clone := *req
	return &clone, nil
}

type fakePresence struct {
	mu        sync.Mutex
	available map[MediaType]map[int]bool
	byTVDB    map[MediaType]map[int]int
	got       []PresenceCandidate
	// seasons holds per-season counts by series content ID.
	seasons map[string]map[int]SeasonCounts
	// seasonErr fails SeasonAvailability.
	seasonErr     error
	seasonLookups int
}

func (f *fakePresence) SeasonAvailability(_ context.Context, seriesContentIDs []string) (map[string]map[int]SeasonCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seasonLookups++
	if f.seasonErr != nil {
		return nil, f.seasonErr
	}
	out := map[string]map[int]SeasonCounts{}
	for _, id := range seriesContentIDs {
		if counts, ok := f.seasons[id]; ok {
			out[id] = counts
		}
	}
	return out, nil
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
	mu                sync.Mutex
	page              *tmdb.MediaPage
	externalIDs       *tmdb.ExternalIDs
	externalIDsErr    error
	externalIDsByID   map[int]*tmdb.ExternalIDs
	externalIDCalls   []int
	detail            *tmdb.MediaDetail
	detailErr         error
	detailCalls       int
	discoverPage      *tmdb.MediaPage
	discoverErr       error
	searchMediaType   string
	gotDiscoverParams tmdb.DiscoverParams
}

func (f *fakeTMDBClient) SearchMedia(_ context.Context, mediaType, _ string, _ int) (*tmdb.MediaPage, error) {
	f.searchMediaType = mediaType
	return f.page, nil
}

func (f *fakeTMDBClient) ChartPage(context.Context, tmdb.ChartParams, int) (*tmdb.MediaPage, error) {
	return f.page, nil
}

func (f *fakeTMDBClient) DiscoverPage(_ context.Context, _ string, params tmdb.DiscoverParams, _ int) (*tmdb.MediaPage, error) {
	f.mu.Lock()
	f.gotDiscoverParams = params
	f.mu.Unlock()
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
	if f.externalIDsErr != nil {
		return nil, f.externalIDsErr
	}
	if f.externalIDsByID != nil {
		return f.externalIDsByID[id], nil
	}
	return f.externalIDs, nil
}

func (f *fakeTMDBClient) GetMediaDetail(context.Context, string, int) (*tmdb.MediaDetail, error) {
	f.mu.Lock()
	f.detailCalls++
	f.mu.Unlock()
	return f.detail, f.detailErr
}

// certTMDBClient layers GetCertification onto fakeTMDBClient so a service
// under a rating ceiling can hydrate certifications. Kept separate from
// fakeTMDBClient so tests without certifications pin that the plain client
// does NOT satisfy TMDBCertificationClient.
type certTMDBClient struct {
	fakeTMDBClient
	certs     map[int]string // tmdb id -> certification
	certErr   error
	certCalls atomic.Int64
}

func (f *certTMDBClient) GetCertification(_ context.Context, _ string, id int) (string, error) {
	f.certCalls.Add(1)
	if f.certErr != nil {
		return "", f.certErr
	}
	return f.certs[id], nil
}

type fixedCeiling struct{ q string }

// failingCeiling is an entitlement resolver whose lookup always errors.
type failingCeiling struct{}

func (failingCeiling) MaxPlaybackQuality(context.Context, int, string) (string, error) {
	return "", errors.New("entitlement lookup failed")
}

func (f fixedCeiling) MaxPlaybackQuality(context.Context, int, string) (string, error) {
	return f.q, nil
}

// ratedCeiling implements both EntitlementResolver and ContentRatingResolver.
type ratedCeiling struct {
	q         string
	rating    string
	ratingErr error
}

func (f ratedCeiling) MaxPlaybackQuality(context.Context, int, string) (string, error) {
	return f.q, nil
}

func (f ratedCeiling) MaxContentRating(context.Context, int, string) (string, error) {
	return f.rating, f.ratingErr
}

// fakeRouterProvider is a canned RequestRouterProvider standing in for a
// request_router.v1 plugin. Fulfill emits one target per requested quality
// (unless noTargets is set), recording the qualities and connections it saw.
// fulfillCall and statusCall record one plugin call each.
type fulfillCall struct {
	installationID int
	qualities      []Quality
	conns          []ResolvedRouterConnection
}

type statusCall struct {
	installationID int
	capabilityID   string
	refs           []RouterTargetRef
	conns          []ResolvedRouterConnection
}

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
	fulfillLog        []fulfillCall

	gotRequesterEmail    string
	gotRequesterUsername string
	// gotSeasons records the seasons of each Fulfill call's request.
	gotSeasons [][]int

	// seasonCapable marks the installations whose router declares
	// supports_seasons; RouterFeatures answers from it.
	seasonCapable map[int]bool
	// progressCapable marks the installations whose router declares
	// reports_download_progress.
	progressCapable map[int]bool
	// keyOptional marks the installations whose router declares
	// api_key_optional.
	keyOptional map[int]bool
	// urlOptional marks the installations whose router declares
	// base_url_optional.
	urlOptional map[int]bool
	featuresErr error
	gotTVDBID   *int

	// CheckStatus behavior.
	statuses  []RouterTargetStatus
	statusErr error
	// statusErrFor fails CheckStatus for one installation only.
	statusErrFor map[int]error
	// statusHangFor makes CheckStatus for an installation wait until its
	// context ends, the way a call to a server that stopped answering runs to
	// its deadline.
	statusHangFor map[int]bool
	statusCalls   int
	statusLog     []statusCall

	// ListConfigOptions behavior.
	options        map[string][]RouterOption
	optionsErr     error
	gotOptionsConn ResolvedRouterConnection

	// TestConnection behavior: testFails answers not ok with testMessage;
	// testErr fails the call itself.
	testFails   bool
	testMessage string
	testErr     error
	testCalls   int
	gotTestConn ResolvedRouterConnection

	// Validate behavior (default empty = valid).
	validateFieldErrors   map[string]string
	validateFormError     string
	validateErr           error
	validateCalls         int
	gotValidateCapability string
	gotValidateConn       ResolvedRouterConnection
	gotValidateSiblings   []ResolvedRouterConnection
}

func (f *fakeRouterProvider) RouterFeatures(_ context.Context, installationID int, _ string) (RouterFeatures, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return RouterFeatures{
		SupportsSeasons:         f.seasonCapable[installationID],
		ReportsDownloadProgress: f.progressCapable[installationID],
		APIKeyOptional:          f.keyOptional[installationID],
		BaseURLOptional:         f.urlOptional[installationID],
	}, f.featuresErr
}

func (f *fakeRouterProvider) Fulfill(_ context.Context, installationID int, _ string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotRequesterEmail = req.RequesterEmail
	f.gotRequesterUsername = req.RequesterUsername
	var seasons []int
	for _, season := range routerDescriptor(req).GetSeasons() {
		seasons = append(seasons, int(season))
	}
	f.gotSeasons = append(f.gotSeasons, seasons)
	f.gotTVDBID = req.TVDBID
	f.fulfillCalls++
	f.fulfillLog = append(f.fulfillLog, fulfillCall{installationID: installationID, qualities: slices.Clone(qualities), conns: slices.Clone(conns)})
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

func (f *fakeRouterProvider) CheckStatus(ctx context.Context, installationID int, capabilityID string, _ Request, refs []RouterTargetRef, conns []ResolvedRouterConnection) ([]RouterTargetStatus, error) {
	f.mu.Lock()
	f.statusCalls++
	f.statusLog = append(f.statusLog, statusCall{installationID: installationID, capabilityID: capabilityID, refs: slices.Clone(refs), conns: slices.Clone(conns)})
	hang := f.statusHangFor[installationID]
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.statusErrFor[installationID]; err != nil {
		return nil, err
	}
	return f.statuses, f.statusErr
}

func (f *fakeRouterProvider) ListConfigOptions(_ context.Context, _ int, _ string, conn ResolvedRouterConnection) (map[string][]RouterOption, error) {
	f.mu.Lock()
	f.gotOptionsConn = conn
	f.mu.Unlock()
	if f.optionsErr != nil {
		return nil, f.optionsErr
	}
	return f.options, nil
}

func (f *fakeRouterProvider) TestConnection(_ context.Context, _ int, _ string, conn ResolvedRouterConnection) (bool, string, error) {
	f.mu.Lock()
	f.testCalls++
	f.gotTestConn = conn
	f.mu.Unlock()
	if f.testErr != nil {
		return false, "", f.testErr
	}
	return !f.testFails, f.testMessage, nil
}

func (f *fakeRouterProvider) Validate(_ context.Context, _ int, capabilityID string, conn ResolvedRouterConnection, siblings []ResolvedRouterConnection) (map[string]string, string, error) {
	f.mu.Lock()
	f.validateCalls++
	f.gotValidateCapability = capabilityID
	f.gotValidateConn = conn
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

// autoApproveRouterInst is an enabled request_router connection bound to an
// installation, with the given api key.
func autoApproveRouterInst(id, apiKeyRef string) Integration {
	in := routerInst(id)
	in.APIKeyRef = apiKeyRef
	return in
}

func TestSubmitApprovedIsIdempotentPerQuality(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	svc, router := newRoutedService(store)
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

func TestSubmitApprovedContainsToSingleInstallation(t *testing.T) {
	store := newFakeStore()
	// Two enabled router connections on DIFFERENT installations. Only the first
	// installation's connections may be sent to that plugin.
	store.integrations = []Integration{
		routerInstOn("router-a", 1),
		routerInstOn("router-b", 2),
	}
	svc, router := newRoutedService(store)

	mustSubmitApprovedMovie(t, svc, store)
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
	service, router := newRoutedService(store)
	service.SetEntitlementResolver(fixedCeiling{q: "1080p"}) // single quality, keep it simple

	mustSubmitApprovedMovie(t, service, store)
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

	got, err := submitApprovedMovie(svc, store)
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

type fakeRequesterIdentity struct {
	email, username string
	err             error
	gotUserID       int
}

func (f *fakeRequesterIdentity) ResolveRequester(_ context.Context, userID int) (string, string, error) {
	f.gotUserID = userID
	return f.email, f.username, f.err
}

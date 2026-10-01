package requests

import (
	"context"
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

func TestCreateRequestGroupPolicyCanForbidRequests(t *testing.T) {
	store := newFakeStore()
	service := newTestService(store)
	service.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: false}})

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created requests = %d, want 0", len(store.created))
	}
}

func TestCreateRequestAccountOverrideBeatsGroupDeny(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	service := newTestService(store)
	groupID := int64(1)
	allow := true
	service.SetUserRepository(requestUserRepo{user: &models.User{ID: 1, AccessGroupID: &groupID, RequestsAllowed: &allow}})
	service.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: false}})

	if _, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	}); err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
}

func TestCreateRequestFailsWithoutUserRepository(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	service := NewService(store, &fakeTMDBClient{}, &fakePresence{})

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err == nil {
		t.Fatal("CreateRequest without a user repository should fail rather than skip the per-user gate")
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

// Auto-approval does not wait for a router: on a server without Sonarr/Radarr
// the approved request waits for the title to reach the library (AC3, AC4).
func TestCreateRequestAutoApprovesWithoutRouter(t *testing.T) {
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
	if req.Status != StatusApproved || req.Outcome != OutcomeActive || req.LastError != "" {
		t.Fatalf("request = %+v, want approved and waiting for the library", req)
	}
}

// A keyless connection is a setup problem: the auto-approved request keeps its
// approval and records why it could not be sent, instead of failing, so it goes
// through once an admin adds the key.
func TestCreateRequestAutoApprovalDefersOnKeylessConnection(t *testing.T) {
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
	if req.Status != StatusApproved || req.Outcome != OutcomeActive ||
		req.LastError != msgRouterNoKey {
		t.Fatalf("request = %+v, want approved with the missing-key reason recorded", req)
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
	// A router connection that only serves series is never handed a movie
	// request; the auto-approved movie waits for the library instead.
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
	if req.Status != StatusApproved || req.LastError != "" {
		t.Fatalf("request = %+v, want approved and waiting (no router connection supports movie)", req)
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

func TestCreateRequestResolvesSeriesTVDBIDThroughMetadataWhenTMDBHasNone(t *testing.T) {
	store := newFakeStore()
	tmdbClient := &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{IMDbID: "tt31000000"}}
	service := newTestServiceWithTMDB(store, tmdbClient)
	resolver := &fakeTVDBResolver{tvdbID: 456789}
	service.SetTVDBIDResolver(resolver)

	if _, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries,
		TMDBID:    240001,
		Title:     "Regional Series",
	}); err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if resolver.gotTMDBID != 240001 || resolver.gotIMDbID != "tt31000000" {
		t.Fatalf("resolver got tmdb=%d imdb=%q, want 240001 and the TMDB-provided IMDb id", resolver.gotTMDBID, resolver.gotIMDbID)
	}
	if got := store.created[0].Input.TVDBID; got == nil || *got != 456789 {
		t.Fatalf("tvdb_id = %v, want 456789", got)
	}
}

func TestAutoApprovedCreateLooksUpTVDBIDOnce(t *testing.T) {
	store := newFakeStore()
	store.settings.GlobalAutoApprovalEnabled = true
	store.integrations = []Integration{routerInst("router-1")}
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{})
	resolver := &fakeTVDBResolver{}
	service.SetTVDBIDResolver(resolver)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries, TMDBID: 4436, Title: "Unlinked Series",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status == StatusPending {
		t.Fatalf("request stayed pending; the test needs the auto-approve submit path")
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 across create and the immediate submission", resolver.calls)
	}
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

func TestFailedTMDBRefreshKeepsCachedIMDbID(t *testing.T) {
	client := &refreshingTMDBClient{
		fakeTMDBClient: &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{IMDbID: "tt31000000"}},
		refreshErr:     errors.New("tmdb: HTTP 429"),
	}
	service := NewService(newFakeStore(), client, &fakePresence{})
	resolver := &fakeTVDBResolver{tvdbID: 456789}
	service.SetTVDBIDResolver(resolver)

	input := CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 240001}
	service.enrichExternalIDs(context.Background(), &input)
	if resolver.gotIMDbID != "tt31000000" || input.IMDbID != "tt31000000" {
		t.Fatalf("resolver imdb = %q, stored imdb = %q; want the cached TMDB IMDb id kept", resolver.gotIMDbID, input.IMDbID)
	}
	if input.TVDBID == nil || *input.TVDBID != 456789 {
		t.Fatalf("tvdb_id = %v, want 456789 resolved through the cached IMDb id", input.TVDBID)
	}

	unresolved := &fakeTVDBResolver{}
	service.SetTVDBIDResolver(unresolved)
	input = CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 240001}
	if !service.enrichExternalIDs(context.Background(), &input) {
		t.Fatalf("enrichExternalIDs reported a confirmed miss after the TMDB refresh failed")
	}
}

func TestRetryRefreshesTMDBExternalIDs(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["req-1"] = &Request{
		ID: "req-1", MediaType: MediaTypeSeries, TMDBID: 240001,
		Status: StatusQueued, Outcome: OutcomeFailed,
	}
	// The cached lookup still has no TVDB ID; TMDB itself now does.
	client := &refreshingTMDBClient{
		fakeTMDBClient: &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{}},
		fresh:          &tmdb.ExternalIDs{TVDBID: 456789},
	}
	service := NewService(store, client, &fakePresence{})
	service.SetUserRepository(requestUserRepo{})
	router := &fakeRouterProvider{}
	service.SetRouterProvider(router)

	if _, err := service.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1"); err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if client.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", client.refreshCalls)
	}
	if router.gotTVDBID == nil || *router.gotTVDBID != 456789 {
		t.Fatalf("router got tvdb_id %v, want the refreshed 456789", router.gotTVDBID)
	}
}

func TestCreateRequestRefreshesCachedTMDBMiss(t *testing.T) {
	store := newFakeStore()
	client := &refreshingTMDBClient{
		fakeTMDBClient: &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{}},
		fresh:          &tmdb.ExternalIDs{TVDBID: 456789},
	}
	service := NewService(store, client, &fakePresence{})
	service.SetUserRepository(requestUserRepo{})

	if _, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries, TMDBID: 240001, Title: "Regional Series",
	}); err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if got := store.created[0].Input.TVDBID; got == nil || *got != 456789 {
		t.Fatalf("tvdb_id = %v, want the refreshed 456789", got)
	}
}

func TestRetrySubmitsTheSavedTVDBID(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	// A concurrent submission already saved a TVDB ID after this one loaded
	// the request without it.
	saved := 111
	store.requests["req-1"] = &Request{
		ID: "req-1", MediaType: MediaTypeSeries, TMDBID: 240001,
		Status: StatusApproved, Outcome: OutcomeActive,
	}
	service := newTestService(store)
	router := &fakeRouterProvider{}
	service.SetRouterProvider(router)
	service.SetTVDBIDResolver(&fakeTVDBResolver{tvdbID: 456789})
	req := *store.requests["req-1"]
	store.requests["req-1"].TVDBID = &saved

	if _, err := service.submitApprovedRequest(context.Background(), req, Viewer{UserID: 1, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submitApprovedRequest returned error: %v", err)
	}
	if router.gotTVDBID == nil || *router.gotTVDBID != saved {
		t.Fatalf("router got tvdb_id %v, want the saved %d", router.gotTVDBID, saved)
	}
}

func TestCreateRequestSkipsTVDBResolverWhenTMDBHasTVDBID(t *testing.T) {
	store := newFakeStore()
	service := newTestServiceWithTMDB(store, &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{TVDBID: 12345}})
	resolver := &fakeTVDBResolver{tvdbID: 99}
	service.SetTVDBIDResolver(resolver)

	if _, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries, TMDBID: 1399, Title: "Game of Thrones",
	}); err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0 when TMDB already has the TVDB id", resolver.calls)
	}
}

func TestRetryResolvesMissingSeriesTVDBIDBeforeSubmitting(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["req-1"] = &Request{
		ID: "req-1", MediaType: MediaTypeSeries, TMDBID: 240001,
		Status: StatusQueued, Outcome: OutcomeFailed,
	}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)
	service.SetTVDBIDResolver(&fakeTVDBResolver{tvdbID: 456789})

	if _, err := service.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1"); err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if router.gotTVDBID == nil || *router.gotTVDBID != 456789 {
		t.Fatalf("router got tvdb_id %v, want 456789", router.gotTVDBID)
	}
	if got := store.requests["req-1"].TVDBID; got == nil || *got != 456789 {
		t.Fatalf("stored tvdb_id = %v, want the resolved id recorded on the request", got)
	}
}

func TestSubmitExplainsSeriesWithoutTVDBID(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["req-1"] = &Request{
		ID: "req-1", MediaType: MediaTypeSeries, TMDBID: 240001,
		Status: StatusQueued, Outcome: OutcomeFailed,
	}
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{{
		Quality: Quality1080p, ConnectionID: "router-1", Status: StatusFailed,
		Message: "sonarr: tvdb_id is required",
	}}}
	service := newTestService(store)
	service.SetRouterProvider(router)
	service.SetTVDBIDResolver(&fakeTVDBResolver{})

	req, err := service.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1")
	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if req.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want failed", req.Outcome)
	}
	var explained bool
	for _, target := range store.targets["req-1"] {
		if target.Quality == Quality1080p {
			explained = target.LastError == missingTVDBIDMessage
		}
	}
	if !explained {
		t.Fatalf("targets = %+v, want the 1080p target to carry the missing-TVDB explanation", store.targets["req-1"])
	}
}

func TestSubmitKeepsUnrelatedSeriesFailureMessage(t *testing.T) {
	req := Request{MediaType: MediaTypeSeries}
	for _, msg := range []string{"sonarr: quality profile is required", "tvdb: HTTP 503 service unavailable", "tvdb API key is missing"} {
		if got := explainSubmissionFailure(req, msg); got != msg {
			t.Fatalf("message = %q, want the backend message %q unchanged", got, msg)
		}
	}
	tvdbID := 1
	req.TVDBID = &tvdbID
	if got := explainSubmissionFailure(req, "sonarr: tvdb lookup failed"); got != "sonarr: tvdb lookup failed" {
		t.Fatalf("message = %q, want unchanged when the request has a TVDB id", got)
	}
}

func TestExplainSubmissionFailureDistinguishesLookupFailure(t *testing.T) {
	req := Request{MediaType: MediaTypeSeries}
	if got := explainSubmissionFailure(req, "sonarr: tvdb_id is required"); got != missingTVDBIDMessage {
		t.Fatalf("message = %q, want the missing-ID explanation", got)
	}
	req.tvdbLookupFailed = true
	if got := explainSubmissionFailure(req, "sonarr: tvdb_id is required"); got != tvdbLookupFailedMessage {
		t.Fatalf("message = %q, want the lookup-failed explanation", got)
	}
}

func TestCreateRequestTreatsZeroTVDBIDAsMissing(t *testing.T) {
	store := newFakeStore()
	service := newTestService(store)
	resolver := &fakeTVDBResolver{tvdbID: 456789}
	service.SetTVDBIDResolver(resolver)
	zero := 0

	if _, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries, TMDBID: 240001, TVDBID: &zero, Title: "Regional Series",
	}); err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if got := store.created[0].Input.TVDBID; got == nil || *got != 456789 {
		t.Fatalf("tvdb_id = %v, want the resolved 456789 in place of 0", got)
	}
}

func TestRetryStopsWhenResolvedTVDBIDCannotBeSaved(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.setExternalIDsErr = errors.New("db unavailable")
	store.requests["req-1"] = &Request{
		ID: "req-1", MediaType: MediaTypeSeries, TMDBID: 240001,
		Status: StatusQueued, Outcome: OutcomeFailed,
	}
	router := &fakeRouterProvider{}
	service := newTestService(store)
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
		t.Fatalf("fulfill calls = %d, want 0 when the resolved ID was not saved", router.fulfillCalls)
	}
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
	service.SetRouterProvider(&fakeRouterProvider{})
	service.SetTVDBIDResolver(&fakeTVDBResolver{tvdbID: 456789})

	// The reopened approval stands; the save error defers the submission.
	got, err := service.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1")
	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if !strings.Contains(got.LastError, "db unavailable") {
		t.Fatalf("last_error = %q, want the save error", got.LastError)
	}
	if got := store.targets["req-1"]; len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("targets = %+v, want the failed target kept", got)
	}
}

func TestTMDBFailureCountsAsFailedTVDBLookup(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{externalIDsErr: errors.New("tmdb: HTTP 429")})
	service.SetTVDBIDResolver(&fakeTVDBResolver{})

	input := CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 240001}
	if !service.enrichExternalIDs(context.Background(), &input) {
		t.Fatalf("enrichExternalIDs reported a confirmed miss, want a failed lookup when TMDB errored")
	}
	resolved := &fakeTVDBResolver{tvdbID: 456789}
	service.SetTVDBIDResolver(resolved)
	input = CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 240001}
	if service.enrichExternalIDs(context.Background(), &input) {
		t.Fatalf("enrichExternalIDs reported a failure although the providers resolved the ID")
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
		ID:                "req-prior-failed",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Outcome:           OutcomeFailed,
		Status:            StatusApproved,
		RequestedByUserID: 1,
		LastError:         "arr: decode response: json: cannot unmarshal object into Go value of type []radarr.movieResource",
	}
	store.requests["req-other-media-failed"] = &Request{
		ID:                "req-other-media-failed",
		MediaType:         MediaTypeMovie,
		TMDBID:            999,
		Outcome:           OutcomeFailed,
		RequestedByUserID: 1,
	}
	// Another account's failed request for the same title is their history
	// and their quota; a re-request must leave it alone.
	store.requests["req-other-user-failed"] = &Request{
		ID:                "req-other-user-failed",
		MediaType:         MediaTypeMovie,
		TMDBID:            550,
		Outcome:           OutcomeFailed,
		RequestedByUserID: 2,
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
	if _, ok := store.requests["req-other-user-failed"]; !ok {
		t.Fatal("another user's failed request for the same media must not be cleared")
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

// A router that never reports completion would otherwise pin the request open
// forever, because the presence shortcut stays disabled while a target is live.
func TestReconcileRequestsRetiresStalledQueuedTargetOnPresence(t *testing.T) {
	store, service, _ := seedStalledPresenceRequest(t, StatusQueued, 48*time.Hour)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("result = %+v, want one completed", result)
	}
	targets, _ := store.ListTargets(context.Background(), "req-1")
	if len(targets) != 1 || targets[0].Status != StatusCompleted {
		t.Fatalf("targets = %+v, want the stalled target completed", targets)
	}
	if targets[0].ExternalStatus != ExternalStatusPresenceConfirmed {
		t.Fatalf("external status = %q, want %s", targets[0].ExternalStatus, ExternalStatusPresenceConfirmed)
	}
	if store.requests["req-1"].Status != StatusCompleted {
		t.Fatalf("request status = %q, want completed", store.requests["req-1"].Status)
	}
}

// Inside the horizon the router still owns the target — presence must not
// short-circuit a submission that may simply be young.
func TestReconcileRequestsKeepsRecentQueuedTargetOnPresence(t *testing.T) {
	store, service, _ := seedStalledPresenceRequest(t, StatusQueued, time.Hour)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Completed != 0 {
		t.Fatalf("result = %+v, want nothing completed", result)
	}
	targets, _ := store.ListTargets(context.Background(), "req-1")
	if targets[0].Status != StatusQueued {
		t.Fatalf("target status = %q, want queued", targets[0].Status)
	}
}

// The presence check is quality-agnostic, so a 2160p target still downloading
// against an already-present 1080p copy must never be retired out from under
// the in-flight download.
func TestReconcileRequestsNeverRetiresDownloadingTargetOnPresence(t *testing.T) {
	store, service, _ := seedStalledPresenceRequest(t, StatusDownloading, 30*24*time.Hour)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Completed != 0 {
		t.Fatalf("result = %+v, want nothing completed", result)
	}
	targets, _ := store.ListTargets(context.Background(), "req-1")
	if targets[0].Status != StatusDownloading {
		t.Fatalf("target status = %q, want downloading left alone", targets[0].Status)
	}
}

// Partial retirement: the stalled quality is closed out while a sibling target
// that is genuinely downloading keeps the request open.
func TestReconcileRequestsRetiresStalledQueuedWhileDownloadingStaysOpen(t *testing.T) {
	store, service, now := seedStalledPresenceRequest(t, StatusQueued, 48*time.Hour)
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: "req-1", IntegrationID: "router-1", IntegrationKind: "radarr",
		Quality: Quality2160p, Status: StatusDownloading, ExternalID: "456", ExternalStatus: "downloading",
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Completed != 0 {
		t.Fatalf("result = %+v, want nothing completed while a download is in flight", result)
	}

	targets, _ := store.ListTargets(context.Background(), "req-1")
	byQuality := map[Quality]Target{}
	for _, t := range targets {
		byQuality[t.Quality] = t
	}
	if got := byQuality[Quality1080p]; got.Status != StatusCompleted || got.ExternalStatus != ExternalStatusPresenceConfirmed {
		t.Fatalf("1080p target = %+v, want completed via presence", got)
	}
	if got := byQuality[Quality2160p]; got.Status != StatusDownloading {
		t.Fatalf("2160p target = %+v, want left downloading", got)
	}
	if store.requests["req-1"].Status != StatusDownloading {
		t.Fatalf("request status = %q, want downloading (the live target keeps it open)", store.requests["req-1"].Status)
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

// An integration the host cannot reach is a dependency failure, not an
// internal one: the service reports it with a sentinel the API layer maps to
// an upstream-unavailable status.
func TestLoadIntegrationOptionsClassifiesUnreachableIntegration(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{optionsErr: errors.New("dial tcp: connect: connection refused")}
	service := newTestService(store)
	service.SetRouterProvider(router)

	_, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{ID: "router-1", BaseURL: "http://router-1.local"})
	if !errors.Is(err, ErrIntegrationUnreachable) {
		t.Fatalf("err = %v, want ErrIntegrationUnreachable", err)
	}
}

// A plugin's validation result stays a client problem.
func TestLoadIntegrationOptionsKeepsValidationErrors(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{optionsErr: &ValidationError{FormError: "api key rejected"}}
	service := newTestService(store)
	service.SetRouterProvider(router)

	_, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{ID: "router-1", BaseURL: "http://router-1.local"})
	var validation *ValidationError
	if !errors.As(err, &validation) || errors.Is(err, ErrIntegrationUnreachable) {
		t.Fatalf("err = %v, want the plugin validation error", err)
	}
	// Returned as is, not as a host-classified probe error.
	var probe *ProbeValidationError
	if errors.As(err, &probe) {
		t.Fatalf("err = %v, want the router's error unchanged", err)
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

func TestDeclineRejectsApprovedRequests(t *testing.T) {
	store := newFakeStore()
	inFlight := time.Now().Add(time.Minute)
	store.requests["req-approved"] = &Request{
		ID:               "req-approved",
		MediaType:        MediaTypeMovie,
		TMDBID:           550,
		Status:           StatusApproved,
		Outcome:          OutcomeActive,
		SubmitLeaseUntil: &inFlight,
	}
	service := newTestService(store)

	_, err := service.Decline(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-approved", "changed mind")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState (a submission is in flight)", err)
	}
}

// An approved request nothing was sent for (no router, or backing off after a
// failed attempt) can still be declined by an admin or withdrawn by its owner;
// otherwise it could never be closed.
func TestWithdrawApprovedRequestNothingWasSentFor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withdraw func(*Service) (*Request, error)
		want     Outcome
	}{
		{"decline", func(s *Service) (*Request, error) {
			return s.Decline(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-x", "not this month")
		}, OutcomeDeclined},
		{"cancel", func(s *Service) (*Request, error) {
			return s.Cancel(context.Background(), Viewer{UserID: 7, ProfileID: "profile-1"}, "req-x", "")
		}, OutcomeCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			backoff := time.Now().Add(time.Hour)
			store.requests["req-x"] = &Request{
				ID: "req-x", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive,
				RequestedByUserID: 7, NextSubmitAt: &backoff, LastError: "radarr unreachable",
			}
			got, err := tc.withdraw(newTestService(store))
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
		})
	}
}

func TestDeclineRejectsApprovedRequestWithTarget(t *testing.T) {
	store := newFakeStore()
	store.requests["req-x"] = &Request{ID: "req-x", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive}
	store.targets = map[string][]Target{"req-x": {{ID: 1, RequestID: "req-x", Quality: Quality1080p, Status: StatusQueued}}}

	_, err := newTestService(store).Decline(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-x", "")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("err = %v, want ErrInvalidState (a target exists)", err)
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
	service.SetUserRepository(requestUserRepo{})
	return service
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

func (f *fakeStore) ListMine(context.Context, int, ListFilter) ([]*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*Request(nil), f.mine...), nil
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
		return !leased && len(f.targets[req.ID]) == 0
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
	claimed := req.Status == StatusApproved && req.SubmitLeaseUntil != nil && req.SubmitLeaseUntil.After(time.Now())
	partlyDelivered := false
	for _, t := range f.targets[id] {
		if t.Status == StatusCompleted {
			partlyDelivered = true
		}
	}
	failedElsewhere := req.Outcome == OutcomeFailed && !partlyDelivered
	if !failedElsewhere && (req.Outcome != OutcomeActive || !open || claimed) {
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

func (f *fakeStore) RecomputeStatus(_ context.Context, id string, _ Viewer) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.lookupLocked(id)
	if req == nil {
		return nil, ErrNotFound
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive {
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

// seedFollowFor records a follow waiting for a given request.
func (f *fakeStore) seedFollowFor(mediaType MediaType, tmdbID int, viewer Viewer, requestID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	if f.factsSet == nil {
		f.factsSet = map[string]RoutingFacts{}
	}
	f.factsSet[id] = facts
	req.RoutingFacts = facts
	req.IsAnime = facts.Anime
	copy := *req
	return &copy, nil
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

// RecordSubmission mirrors the repository's lease fence, then records each
// target the way a create followed by a status update would.
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
	// seasons holds per-season counts by series content ID.
	seasons       map[string]map[int]SeasonCounts
	seasonLookups int
}

func (f *fakePresence) SeasonAvailability(_ context.Context, seriesContentIDs []string) (map[string]map[int]SeasonCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seasonLookups++
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
	discoverPage      *tmdb.MediaPage
	discoverErr       error
	searchMediaType   string
	gotDiscoverParams tmdb.DiscoverParams
}

func (f *fakeTMDBClient) SearchMedia(_ context.Context, mediaType, _ string, _ int) (*tmdb.MediaPage, error) {
	f.searchMediaType = mediaType
	return f.page, nil
}

func (f *fakeTMDBClient) DiscoverSection(context.Context, string, int) (*tmdb.MediaPage, error) {
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
	featuresErr     error
	gotTVDBID       *int

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

	// Validate behavior (default empty = valid).
	validateFieldErrors   map[string]string
	validateFormError     string
	validateErr           error
	validateCalls         int
	gotValidateCapability string
	gotValidateSiblings   []ResolvedRouterConnection
}

func (f *fakeRouterProvider) RouterFeatures(_ context.Context, installationID int, _ string) (RouterFeatures, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return RouterFeatures{
		SupportsSeasons:         f.seasonCapable[installationID],
		ReportsDownloadProgress: f.progressCapable[installationID],
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

// autoApproveRouterInst is an enabled request_router connection bound to an
// installation, with the given api key.
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
		got, _ := svcHD.allowedQualities(context.Background(), Request{}, Settings{})
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
		got, _ := svcAny.allowedQualities(context.Background(), Request{}, Settings{})
		if len(got) != 2 || got[1] != Quality2160p {
			t.Fatalf("qualities = %v, want [1080p 2160p]", got)
		}
	})

	t.Run("force dual adds 2160p", func(t *testing.T) {
		got, _ := svc.allowedQualities(context.Background(), Request{}, Settings{ForceDualQuality: true})
		if len(got) != 2 || got[1] != Quality2160p {
			t.Fatalf("qualities = %v, want [1080p 2160p]", got)
		}
	})

	t.Run("4k ceiling adds 2160p", func(t *testing.T) {
		svc4k := newTestService(newFakeStore())
		svc4k.SetEntitlementResolver(fixedCeiling{q: "2160p"})
		got, _ := svc4k.allowedQualities(context.Background(), Request{}, Settings{})
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

func TestSubmitApprovedNoRouterWaitsForLibrary(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	svc := newTestService(store) // no router provider set

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.Status != StatusApproved || got.Outcome != OutcomeActive || got.LastError != "" {
		t.Fatalf("request = %+v, want approved and waiting (no router configured)", got)
	}
}

func TestSubmitApprovedNoConnectionsWaitsForLibrary(t *testing.T) {
	store := newFakeStore() // no integrations
	svc := newTestService(store)
	svc.SetRouterProvider(&fakeRouterProvider{})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got.Status != StatusApproved || got.Outcome != OutcomeActive || got.LastError != "" {
		t.Fatalf("request = %+v, want approved and waiting (no enabled router connections)", got)
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
	if failed2160.LastError != msgNoTargetForQuality {
		t.Fatalf("2160p last error = %q, want the no-target message", failed2160.LastError)
	}

	// The failed 2160p target is not "healthy", so a re-run (Retry / reconcile)
	// re-attempts only that quality. Provide a normal provider for the re-run.
	retryRouter := &fakeRouterProvider{}
	svc.SetRouterProvider(retryRouter)
	// Put the stored row back where ReopenFailed leaves it, so the re-run can
	// claim the submission.
	store.requests["r1"].Status = StatusApproved
	store.requests["r1"].Outcome = OutcomeActive
	store.requests["r1"].SubmitLeaseUntil = nil
	store.requests["r1"].NextSubmitAt = nil
	cur := *store.requests["r1"]
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
	// No connection serves movies, so the request waits for the library.
	if got.Status != StatusApproved || got.Outcome != OutcomeActive || got.LastError != "" {
		t.Fatalf("request = %+v, want approved and waiting for the library", got)
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
	if got.Outcome != OutcomeActive || got.LastError != msgRouterNoKey {
		t.Fatalf("request = %+v, want the missing-key reason recorded and a retry scheduled", got)
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

func TestSubmitApprovedUnboundInstallationDefersWithGuidance(t *testing.T) {
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
	if got.Status != StatusApproved || got.Outcome != OutcomeActive ||
		got.LastError != msgRouterUnbound {
		t.Fatalf("request = %+v, want approved with unbound-installation guidance and a retry scheduled", got)
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

func TestDeclineKeepsReasonOnRequest(t *testing.T) {
	store := newFakeStore()
	store.requests["req-1"] = &Request{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusPending, Outcome: OutcomeActive}

	got, err := newTestService(store).Decline(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1", "Not this month")
	if err != nil {
		t.Fatalf("Decline: %v", err)
	}
	if got.OutcomeReason != "Not this month" || got.State() != StateDeclined {
		t.Fatalf("request = %+v, want declined with the reason kept", got)
	}
}

package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// fakeRequests is a MediaRequestService over a fixed request list and
// canned provider answers; it records the last call it saw.
type fakeRequests struct {
	requests []*mediarequests.Request
	err      error
	// detailDownload is the download progress GetDetail reports.
	detailDownload *mediarequests.DownloadProgress

	lastViewer mediarequests.Viewer
	lastFilter mediarequests.ListFilter
	lastCreate mediarequests.CreateRequestInput
	lastCall   string
	lastArgs   []any
}

// fixtureMediaRequest is an approved movie request with one queued target.
// The target names its download server and routing rule, which only an
// admin sees.
func fixtureMediaRequest(id string, tmdbID int) *mediarequests.Request {
	year := 1995
	approved := fixedTime()
	return &mediarequests.Request{
		ID: id, Provider: "tmdb", MediaType: mediarequests.MediaTypeMovie, TMDBID: tmdbID, Title: "Heat", Year: &year,
		Status: mediarequests.StatusApproved, Outcome: mediarequests.OutcomeActive, RequestedByUserID: 1, RequestedByProfileID: "p-owner",
		IntegrationKind: "radarr", Targets: []mediarequests.Target{{
			ID: 42, RequestID: id, IntegrationID: "integration-1", IntegrationKind: "radarr", InstanceName: "Radarr",
			Quality: mediarequests.Quality1080p, ExternalID: "7", ExternalStatus: "queued", Status: mediarequests.StatusQueued,
			RouteName: "Movies", CreatedAt: fixedTime(), UpdatedAt: fixedTime(),
		}},
		CreatedAt: fixedTime(), UpdatedAt: fixedTime(), ApprovedAt: &approved,
	}
}

func fixtureResult(tmdbID int) mediarequests.MediaResult {
	return mediarequests.MediaResult{
		MediaType: mediarequests.MediaTypeMovie, TMDBID: tmdbID, Title: "Heat", Year: 1995, ReleaseDate: "1995-12-15",
		VoteAverage: 8.2, Availability: mediarequests.AvailabilityMissing,
		Request: mediarequests.RequestState{Requestable: true},
	}
}

func (f *fakeRequests) record(viewer mediarequests.Viewer, call string, args ...any) error {
	f.lastViewer, f.lastCall, f.lastArgs = viewer, call, args
	return f.err
}

func (f *fakeRequests) Search(_ context.Context, viewer mediarequests.Viewer, query string, mediaType mediarequests.MediaType, page int) (*mediarequests.MediaPage, error) {
	if err := f.record(viewer, "search", query, mediaType, page); err != nil {
		return nil, err
	}
	return &mediarequests.MediaPage{Page: page, TotalPages: 3, TotalResults: 41, Results: []mediarequests.MediaResult{fixtureResult(949)}}, nil
}

func (f *fakeRequests) Discover(_ context.Context, viewer mediarequests.Viewer, section string, page int) (*mediarequests.DiscoverySection, error) {
	if err := f.record(viewer, "discover", section, page); err != nil {
		return nil, err
	}
	return &mediarequests.DiscoverySection{Key: section, Title: "Trending Movies", Page: page, TotalPages: 500, TotalResults: 10000, Results: []mediarequests.MediaResult{fixtureResult(949)}, NextPage: page + 2}, nil
}

func (f *fakeRequests) DiscoverAll(_ context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverySection, error) {
	if err := f.record(viewer, "discoverAll"); err != nil {
		return nil, err
	}
	return []mediarequests.DiscoverySection{{Key: "trending_movies", Title: "Trending Movies", Page: 1, TotalPages: 500, TotalResults: 10000, Results: []mediarequests.MediaResult{fixtureResult(949)}}}, nil
}

func (f *fakeRequests) GetDetail(_ context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) (*mediarequests.MediaDetail, error) {
	if err := f.record(viewer, "detail", mediaType, tmdbID); err != nil {
		return nil, err
	}
	return &mediarequests.MediaDetail{
		MediaType: mediaType, TMDBID: tmdbID, IMDbID: "tt0113277", Title: "Heat", Year: 1995, Runtime: 170, Genres: []string{"Crime"},
		Cast: []mediarequests.MediaCastMember{{Name: "Al Pacino", Character: "Vincent Hanna"}}, Director: "Michael Mann",
		Recommendations: []mediarequests.MediaResult{fixtureResult(950)}, Availability: mediarequests.AvailabilityAvailable,
		LibraryContentID: "movie:heat-1995", Request: mediarequests.RequestState{Reason: "already_available", Download: f.detailDownload},
	}, nil
}

func (f *fakeRequests) CreateRequest(_ context.Context, viewer mediarequests.Viewer, input mediarequests.CreateRequestInput) (*mediarequests.Request, error) {
	f.lastCreate = input
	if err := f.record(viewer, "create"); err != nil {
		return nil, err
	}
	for _, r := range f.requests {
		if r.TMDBID == input.TMDBID && r.MediaType == input.MediaType {
			return nil, mediarequests.ErrAlreadyRequested
		}
	}
	req := fixtureMediaRequest("r-new", input.TMDBID)
	req.Title, req.Status, req.Targets, req.ApprovedAt = input.Title, mediarequests.StatusPending, nil, nil
	return req, nil
}

func (f *fakeRequests) ListMine(_ context.Context, viewer mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error) {
	f.lastFilter = filter
	if err := f.record(viewer, "listMine"); err != nil {
		return nil, err
	}
	var out []*mediarequests.Request
	for _, r := range f.requests {
		if r.RequestedByUserID != viewer.UserID {
			continue
		}
		if filter.Status != "" && r.Status != filter.Status {
			continue
		}
		if filter.Before != nil && (r.CreatedAt.After(filter.Before.CreatedAt) || (r.CreatedAt.Equal(filter.Before.CreatedAt) && r.ID >= filter.Before.ID)) {
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *mediarequests.Request) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	if filter.Offset >= len(out) {
		return nil, nil
	}
	out = out[filter.Offset:]
	if len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (f *fakeRequests) GetRequest(_ context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error) {
	if err := f.record(viewer, "get", id); err != nil {
		return nil, err
	}
	for _, r := range f.requests {
		if r.ID == id {
			if !viewer.IsAdmin && r.RequestedByUserID != viewer.UserID {
				return nil, mediarequests.ErrForbidden
			}
			return r, nil
		}
	}
	return nil, mediarequests.ErrNotFound
}

func (f *fakeRequests) brands(viewer mediarequests.Viewer, call string) ([]mediarequests.DiscoverBrandCard, error) {
	if err := f.record(viewer, call); err != nil {
		return nil, err
	}
	logo := "https://img.example.test/marvel.png"
	return []mediarequests.DiscoverBrandCard{{TMDBID: 420, Slug: "marvel-studios", DisplayName: "Marvel Studios", LogoURL: &logo, SeriesSupported: true}}, nil
}

func (f *fakeRequests) ListStudios(_ context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	return f.brands(viewer, "studios")
}

func (f *fakeRequests) ListNetworks(_ context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	return f.brands(viewer, "networks")
}

func (f *fakeRequests) ListGenres(_ context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	return f.brands(viewer, "genres")
}

func (f *fakeRequests) browse(viewer mediarequests.Viewer, kind, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	if err := f.record(viewer, "browse-"+kind, slug, mediaType, sort, page); err != nil {
		return nil, err
	}
	if slug == "missing" {
		return nil, mediarequests.ErrNotFound
	}
	return &mediarequests.DiscoverBrowseResponse{Kind: kind, Slug: slug, DisplayName: "Marvel Studios", MediaType: mediaType, Sort: sort, Page: page, TotalPages: 20, Results: []mediarequests.MediaResult{fixtureResult(949)}}, nil
}

func (f *fakeRequests) BrowseStudio(_ context.Context, viewer mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browse(viewer, "studio", slug, mediarequests.MediaTypeMovie, sort, page)
}

func (f *fakeRequests) BrowseNetwork(_ context.Context, viewer mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browse(viewer, "network", slug, mediarequests.MediaTypeSeries, sort, page)
}

func (f *fakeRequests) BrowseGenre(_ context.Context, viewer mediarequests.Viewer, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browse(viewer, "genre", slug, mediaType, sort, page)
}

func (f *fakeRequests) Follow(_ context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) (mediarequests.RequestState, error) {
	if err := f.record(viewer, "follow", mediaType, tmdbID); err != nil {
		return mediarequests.RequestState{}, err
	}
	return mediarequests.RequestState{Status: mediarequests.StatusPending, Reason: "already_requested", Following: true}, nil
}

func (f *fakeRequests) Unfollow(_ context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) error {
	return f.record(viewer, "unfollow", mediaType, tmdbID)
}

func requestDeps(svc *fakeRequests) Dependencies {
	deps := pilotDeps(nil, nil)
	if svc != nil {
		deps.Requests = svc
	}
	return deps
}

func fixtureRequests() *fakeRequests {
	other := fixtureMediaRequest("r-3", 951)
	other.RequestedByUserID, other.RequestedByProfileID = 2, "p-primary"
	pending := fixtureMediaRequest("r-2", 950)
	pending.Status = mediarequests.StatusPending
	// r-1 is downloading, so the fixtures show a request with download
	// progress next to ones without.
	downloading := fixtureMediaRequest("r-1", 949)
	downloading.Status = mediarequests.StatusDownloading
	downloading.Targets[0].Status = mediarequests.StatusDownloading
	downloading.Targets[0].Download = fixtureDownload()
	return &fakeRequests{requests: []*mediarequests.Request{downloading, pending, other}}
}

// fixtureDownload is a 4 GiB download 43% of the way.
func fixtureDownload() *mediarequests.DownloadProgress {
	eta := fixedTime().Add(12 * time.Minute)
	return &mediarequests.DownloadProgress{
		Phase: mediarequests.DownloadPhaseDownloading, BytesTotal: 4294967296, BytesLeft: 2448131358,
		EstimatedCompletion: &eta, Downloads: 1, UpdatedAt: fixedTime(),
	}
}

func decodeBody(t *testing.T, rec interface{ String() string }, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(rec.String()), into); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.String())
	}
}

var requestOwner = with(bearer(memberToken), "X-Profile-Id", "p-owner")

func TestCreateRequest(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))
	body := `{"media_type":"series","tmdb_id":1399,"title":"Game of Thrones","year":2011,"tvdb_id":121361}`
	rec := do(t, h, http.MethodPost, "/api/v2/requests", body, requestOwner)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID, Status, MediaType, Title string
		TMDBID                       int    `json:"tmdb_id"`
		Targets                      []any  `json:"targets"`
		RequestedByUserID            string `json:"requested_by_user_id"`
		CreatedAt                    string `json:"created_at"`
	}
	decodeBody(t, rec.Body, &got)
	if got.ID != "r-new" || got.Status != "pending" || got.TMDBID != 1399 || got.Title != "Game of Thrones" || got.Targets == nil || got.RequestedByUserID != "1" || got.CreatedAt != "2026-01-02T03:04:05.678Z" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if svc.lastViewer != (mediarequests.Viewer{UserID: 1, ProfileID: "p-owner"}) || svc.lastCreate.MediaType != mediarequests.MediaTypeSeries || svc.lastCreate.TVDBID == nil || *svc.lastCreate.TVDBID != 121361 {
		t.Fatalf("viewer %+v create %+v", svc.lastViewer, svc.lastCreate)
	}

	// A second active request for the same media is a 409, never a duplicate.
	rec = do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":949,"title":"Heat"}`, requestOwner)
	requireProblem(t, rec, TypeConflict)

	// Typed validation: the media type enum and a blank title.
	rec = do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"tv","tmdb_id":949,"title":""}`, requestOwner)
	p := requireProblem(t, rec, TypeValidationFailed)
	if len(p.Errors) != 2 {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// An unknown member is refused.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":1,"title":"x","quality":"4k"}`, requestOwner), TypeValidationFailed)

	// A season refused by the service names the seasons field.
	svc.err = &mediarequests.ValidationError{FieldErrors: map[string]string{"seasons": "Season numbers start at 1."}}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"series","tmdb_id":1399,"title":"x","seasons":[0]}`, requestOwner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.seasons" {
		t.Fatalf("errors = %+v, want one at body.seasons", p.Errors)
	}
	svc.err = nil

	// Service decisions render as problems.
	svc.err = mediarequests.QuotaError{Used: 5, Limit: 5, WindowDays: 7}
	rec = do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":7,"title":"x"}`, requestOwner)
	if p := requireProblem(t, rec, TypeRateLimited); !strings.Contains(p.Detail, "5 of 5") {
		t.Fatalf("detail = %q", p.Detail)
	}
	svc.err = mediarequests.ErrRequestsDisabled
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":7,"title":"x"}`, requestOwner), TypeCapabilityDisabled)
	svc.err = mediarequests.ErrUserBlocked
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":7,"title":"x"}`, requestOwner), TypePermissionDenied)
	svc.err = &mediarequests.ValidationError{FieldErrors: map[string]string{"root_folder": "pick one"}, FormError: "Radarr rejected the request"}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":7,"title":"x"}`, requestOwner), TypeValidationFailed)
	if p.Detail != "Radarr rejected the request" || len(p.Errors) != 1 || p.Errors[0].Location != "body.root_folder" {
		t.Fatalf("problem = %+v", p)
	}
}

func TestListMyRequests(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))
	type page struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
		Page struct {
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		} `json:"page"`
	}
	rec := do(t, h, http.MethodGet, "/api/v2/requests/mine", "", requestOwner)
	var got page
	decodeBody(t, rec.Body, &got)
	if rec.Code != 200 || len(got.Items) != 2 || got.Page.HasMore || got.Items[0].ID != "r-2" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if svc.lastFilter.Limit != 51 || svc.lastFilter.Offset != 0 {
		t.Fatalf("filter = %+v", svc.lastFilter)
	}

	// Paging: limit 1 walks both rows through the cursor.
	rec = do(t, h, http.MethodGet, "/api/v2/requests/mine?limit=1", "", requestOwner)
	decodeBody(t, rec.Body, &got)
	if len(got.Items) != 1 || !got.Page.HasMore || got.Page.NextCursor == "" {
		t.Fatalf("first page = %s", rec.Body.String())
	}
	firstCursor := got.Page.NextCursor
	rec = do(t, h, http.MethodGet, "/api/v2/requests/mine?limit=1&cursor="+firstCursor, "", requestOwner)
	decodeBody(t, rec.Body, &got)
	if len(got.Items) != 1 || got.Items[0].ID != "r-1" || got.Page.HasMore {
		t.Fatalf("second page = %s", rec.Body.String())
	}
	// A cursor minted under another filter is refused.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/mine?limit=1&status=pending&cursor="+firstCursor, "", requestOwner), TypeInvalidCursor)

	rec = do(t, h, http.MethodGet, "/api/v2/requests/mine?status=pending", "", requestOwner)
	decodeBody(t, rec.Body, &got)
	if len(got.Items) != 1 || got.Items[0].ID != "r-2" || svc.lastFilter.Status != mediarequests.StatusPending {
		t.Fatalf("%s %+v", rec.Body.String(), svc.lastFilter)
	}
	// Offset paging and unknown statuses are validation failures.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/mine?offset=10", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/mine?status=weird", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/mine?limit=100", "", requestOwner), TypeValidationFailed)
}

func TestGetRequest(t *testing.T) {
	h := newTestHandler(t, requestDeps(fixtureRequests()))
	rec := do(t, h, http.MethodGet, "/api/v2/requests/r-1", "", requestOwner)
	var got struct {
		ID      string `json:"id"`
		Targets []struct {
			ID        string `json:"id"`
			RequestID string `json:"request_id"`
		} `json:"targets"`
		ApprovedAt string `json:"approved_at"`
	}
	decodeBody(t, rec.Body, &got)
	if rec.Code != 200 || got.ID != "r-1" || len(got.Targets) != 1 || got.Targets[0].ID != "42" || got.Targets[0].RequestID != "r-1" || got.ApprovedAt != "2026-01-02T03:04:05.678Z" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/r-404", "", requestOwner), TypeNotFound)
	// Another account's request is forbidden to a member and visible to an admin.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/r-3", "", requestOwner), TypePermissionDenied)
	if rec := do(t, h, http.MethodGet, "/api/v2/requests/r-3", "", with(bearer(adminToken), "X-Profile-Id", "p-primary")); rec.Code != 200 {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body.String())
	}
}

// A request's download server details are for admins only: the servers and
// routing rules a request went to, the servers' own ids and raw statuses, and
// errors that can name them.
var (
	adminRequestMembers = []string{"integration_kind", "external_id", "external_status", "last_error"}
	adminTargetMembers  = []string{"integration_id", "integration_kind", "instance_name", "external_id", "external_status", "last_error", "route_name"}
	// serverRequestMembers and serverTargetMembers are the ones
	// fixtureMediaRequest fills; withSubmissionErrors fills the rest.
	serverRequestMembers = []string{"integration_kind"}
	serverTargetMembers  = []string{"integration_id", "integration_kind", "instance_name", "external_id", "external_status", "route_name"}
	// requesterTargetMembers are what every viewer gets on a target.
	requesterTargetMembers = []string{"id", "request_id", "quality", "is_anime", "status", "created_at", "updated_at"}
)

// withSubmissionErrors adds the rest of the admin details: the errors a failed
// submission leaves, which name a server and a routing rule, and the request's
// own server fields.
func withSubmissionErrors(r *mediarequests.Request) {
	r.ExternalID, r.ExternalStatus = "3", "5"
	r.LastError = `route "Movies" sends to "Radarr", which is disabled`
	for i := range r.Targets {
		r.Targets[i].LastError = `Post "http://radarr.lan:7878/api/v3/movie": connection refused`
	}
}

// requireAdminMembers checks that a request body carries exactly the wanted
// admin-only members, on the request and on each of its targets, and that its
// targets keep what a requester sees.
func requireAdminMembers(t *testing.T, label string, req map[string]any, wantRequest, wantTarget []string) {
	t.Helper()
	for _, m := range adminRequestMembers {
		if _, got := req[m]; got != slices.Contains(wantRequest, m) {
			t.Errorf("%s: request %s present = %v", label, m, got)
		}
	}
	targets, _ := req["targets"].([]any)
	for _, raw := range targets {
		target, _ := raw.(map[string]any)
		for _, m := range adminTargetMembers {
			if _, got := target[m]; got != slices.Contains(wantTarget, m) {
				t.Errorf("%s: target %s present = %v", label, m, got)
			}
		}
		for _, m := range requesterTargetMembers {
			if _, ok := target[m]; !ok {
				t.Errorf("%s: target lost %s", label, m)
			}
		}
	}
}

// requireTargets decodes one request body and checks it has targets.
func requireTargets(t *testing.T, label string, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", label, rec.Code, rec.Body.String())
	}
	var req map[string]any
	decodeBody(t, rec.Body, &req)
	if targets, _ := req["targets"].([]any); len(targets) == 0 {
		t.Fatalf("%s: no targets in %s", label, rec.Body.String())
	}
	return req
}

// The profile-scoped request operations leave the download server details
// out for a requester and keep them for an admin.
func TestRequestDownloadServerDetailsAreForAdmins(t *testing.T) {
	svc := fixtureRequests()
	for _, r := range svc.requests {
		withSubmissionErrors(r)
	}
	deps := requestDeps(svc)
	deps.RequestLifecycle = &fakeLifecycle{}
	h := newTestHandler(t, deps)
	admin := with(bearer(adminToken), "X-Profile-Id", "p-primary")

	// A requester sees none of them, on any operation that answers with a
	// request.
	created := do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":7,"title":"Heat"}`, requestOwner)
	if created.Code != http.StatusCreated {
		t.Fatalf("createRequest: %d %s", created.Code, created.Body.String())
	}
	var body map[string]any
	decodeBody(t, created.Body, &body)
	requireAdminMembers(t, "createRequest", body, nil, nil)

	var mine struct {
		Items []map[string]any `json:"items"`
	}
	rec := do(t, h, http.MethodGet, "/api/v2/requests/mine", "", requestOwner)
	decodeBody(t, rec.Body, &mine)
	if rec.Code != http.StatusOK || len(mine.Items) != 2 {
		t.Fatalf("listMyRequests: %d %s", rec.Code, rec.Body.String())
	}
	for _, item := range mine.Items {
		requireAdminMembers(t, "listMyRequests", item, nil, nil)
	}
	got := requireTargets(t, "getRequest", do(t, h, http.MethodGet, "/api/v2/requests/r-1", "", requestOwner))
	requireAdminMembers(t, "getRequest", got, nil, nil)
	target := got["targets"].([]any)[0].(map[string]any)
	if target["quality"] != "1080p" || target["status"] != "downloading" || target["download"] == nil {
		t.Fatalf("getRequest: target = %v, want its quality, status and download", target)
	}
	got = requireTargets(t, "cancelRequest", do(t, h, http.MethodPost, "/api/v2/requests/r-1/cancel", `{}`, requestOwner))
	requireAdminMembers(t, "cancelRequest", got, nil, nil)

	// An admin sees every one the request has on the same operations. The
	// create and cancel fakes answer with a fresh fixture, which carries no
	// errors and, once created, no targets.
	created = do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":8,"title":"Heat"}`, admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("admin createRequest: %d %s", created.Code, created.Body.String())
	}
	var adminBody map[string]any
	decodeBody(t, created.Body, &adminBody)
	requireAdminMembers(t, "admin createRequest", adminBody, serverRequestMembers, nil)
	var adminMine struct {
		Items []map[string]any `json:"items"`
	}
	rec = do(t, h, http.MethodGet, "/api/v2/requests/mine", "", admin)
	decodeBody(t, rec.Body, &adminMine)
	if rec.Code != http.StatusOK || len(adminMine.Items) != 1 {
		t.Fatalf("admin listMyRequests: %d %s", rec.Code, rec.Body.String())
	}
	requireAdminMembers(t, "admin listMyRequests", adminMine.Items[0], adminRequestMembers, adminTargetMembers)
	got = requireTargets(t, "admin getRequest", do(t, h, http.MethodGet, "/api/v2/requests/r-1", "", admin))
	requireAdminMembers(t, "admin getRequest", got, adminRequestMembers, adminTargetMembers)
	target = got["targets"].([]any)[0].(map[string]any)
	if target["instance_name"] != "Radarr" || target["route_name"] != "Movies" || target["last_error"] != `Post "http://radarr.lan:7878/api/v3/movie": connection refused` {
		t.Fatalf("admin getRequest: target = %v", target)
	}
	got = requireTargets(t, "admin cancelRequest", do(t, h, http.MethodPost, "/api/v2/requests/r-1/cancel", `{}`, admin))
	requireAdminMembers(t, "admin cancelRequest", got, serverRequestMembers, serverTargetMembers)
}

func TestSearchRequestMedia(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))
	rec := do(t, h, http.MethodGet, "/api/v2/requests/search?q=heat&media_type=movie&page=2", "", requestOwner)
	var got struct {
		Page    int `json:"page"`
		Results []struct {
			TMDBID  int `json:"tmdb_id"`
			Request struct {
				Requestable bool `json:"requestable"`
			} `json:"request"`
		} `json:"results"`
	}
	decodeBody(t, rec.Body, &got)
	if rec.Code != 200 || got.Page != 2 || len(got.Results) != 1 || got.Results[0].TMDBID != 949 || !got.Results[0].Request.Requestable {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if svc.lastArgs[0] != "heat" || svc.lastArgs[1] != mediarequests.MediaTypeMovie || svc.lastArgs[2] != 2 {
		t.Fatalf("args = %v", svc.lastArgs)
	}
	// media_type defaults to all; page to 1.
	do(t, h, http.MethodGet, "/api/v2/requests/search?q=heat", "", requestOwner)
	if svc.lastArgs[1] != mediarequests.MediaTypeAll || svc.lastArgs[2] != 1 {
		t.Fatalf("args = %v", svc.lastArgs)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/search", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/search?q=%20", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/search?q=heat&media_type=tv", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/search?q=heat&page=0", "", requestOwner), TypeValidationFailed)
}

func TestGetRequestMediaDetail(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))
	rec := do(t, h, http.MethodGet, "/api/v2/requests/detail/movie/949", "", requestOwner)
	var got struct {
		TMDBID           int      `json:"tmdb_id"`
		Availability     string   `json:"availability"`
		LibraryContentID string   `json:"library_content_id"`
		Networks         []string `json:"networks"`
		Cast             []any    `json:"cast"`
		Recommendations  []any    `json:"recommendations"`
	}
	decodeBody(t, rec.Body, &got)
	if rec.Code != 200 || got.TMDBID != 949 || got.Availability != "available" || got.LibraryContentID != "movie:heat-1995" || got.Networks == nil || len(got.Cast) != 1 || len(got.Recommendations) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if svc.lastArgs[0] != mediarequests.MediaTypeMovie || svc.lastArgs[1] != 949 {
		t.Fatalf("args = %v", svc.lastArgs)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/detail/tv/949", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/detail/movie/0", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/detail/movie/abc", "", requestOwner), TypeValidationFailed)
	svc.err = mediarequests.ErrNotFound
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/detail/movie/949", "", requestOwner), TypeNotFound)
}

func TestDiscover(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))

	rec := do(t, h, http.MethodGet, "/api/v2/requests/discover", "", requestOwner)
	var sections struct {
		Items []struct {
			Key     string `json:"key"`
			Results []any  `json:"results"`
		} `json:"items"`
		Page *any `json:"page"`
	}
	decodeBody(t, rec.Body, &sections)
	if rec.Code != 200 || len(sections.Items) != 1 || sections.Items[0].Key != "trending_movies" || len(sections.Items[0].Results) != 1 || sections.Page != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/api/v2/requests/discover/popular_series?page=3", "", requestOwner)
	var section struct {
		Key      string `json:"key"`
		Page     int    `json:"page"`
		NextPage int    `json:"next_page"`
	}
	decodeBody(t, rec.Body, &section)
	if rec.Code != 200 || section.Key != "popular_series" || section.Page != 3 || section.NextPage != 5 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/discover/weird", "", requestOwner), TypeValidationFailed)

	for _, seg := range []string{"genres", "networks", "studios"} {
		rec = do(t, h, http.MethodGet, "/api/v2/requests/discover/"+seg, "", requestOwner)
		var brands struct {
			Items []struct {
				TMDBID int     `json:"tmdb_id"`
				Slug   string  `json:"slug"`
				Logo   *string `json:"logo_url"`
			} `json:"items"`
		}
		decodeBody(t, rec.Body, &brands)
		if rec.Code != 200 || len(brands.Items) != 1 || brands.Items[0].TMDBID != 420 || brands.Items[0].Slug != "marvel-studios" || brands.Items[0].Logo == nil || svc.lastCall != seg {
			t.Fatalf("%s: %d %s (%s)", seg, rec.Code, rec.Body.String(), svc.lastCall)
		}
	}

	rec = do(t, h, http.MethodGet, "/api/v2/requests/discover/browse/studio/marvel-studios?sort=release_date&page=2", "", requestOwner)
	var browse struct {
		Kind      string `json:"kind"`
		MediaType string `json:"media_type"`
		Sort      string `json:"sort"`
		Page      int    `json:"page"`
		Results   []any  `json:"results"`
	}
	decodeBody(t, rec.Body, &browse)
	if rec.Code != 200 || browse.Kind != "studio" || browse.MediaType != "movie" || browse.Sort != "release_date" || browse.Page != 2 || len(browse.Results) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/requests/discover/browse/network/hbo", "", requestOwner)
	decodeBody(t, rec.Body, &browse)
	if rec.Code != 200 || browse.Kind != "network" || browse.MediaType != "series" || browse.Sort != "popularity" || browse.Page != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/requests/discover/browse/genre/action?media_type=series", "", requestOwner)
	decodeBody(t, rec.Body, &browse)
	if rec.Code != 200 || browse.Kind != "genre" || browse.MediaType != "series" || svc.lastArgs[1] != mediarequests.MediaTypeSeries {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// A genre browse needs a media type; sorts are an enum; an unknown slug is 404.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/discover/browse/genre/action", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/discover/browse/studio/marvel-studios?sort=title", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/discover/browse/studio/%20", "", requestOwner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/requests/discover/browse/studio/missing", "", requestOwner), TypeNotFound)
}

func TestRequestsDenied(t *testing.T) {
	h := newTestHandler(t, requestDeps(fixtureRequests()))
	ops := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v2/requests", `{"media_type":"movie","tmdb_id":1,"title":"x"}`},
		{http.MethodGet, "/api/v2/requests/mine", ""},
		{http.MethodGet, "/api/v2/requests/r-1", ""},
		{http.MethodGet, "/api/v2/requests/search?q=heat", ""},
		{http.MethodGet, "/api/v2/requests/detail/movie/949", ""},
		{http.MethodGet, "/api/v2/requests/discover", ""},
		{http.MethodGet, "/api/v2/requests/discover/trending_movies", ""},
		{http.MethodGet, "/api/v2/requests/discover/genres", ""},
		{http.MethodGet, "/api/v2/requests/discover/networks", ""},
		{http.MethodGet, "/api/v2/requests/discover/studios", ""},
		{http.MethodGet, "/api/v2/requests/discover/browse/genre/action?media_type=movie", ""},
		{http.MethodGet, "/api/v2/requests/discover/browse/network/hbo", ""},
		{http.MethodGet, "/api/v2/requests/discover/browse/studio/marvel-studios", ""},
	}
	for _, op := range ops {
		requireProblem(t, do(t, h, op.method, op.path, op.body, nil), TypeAuthenticationRequired)
		requireProblem(t, do(t, h, op.method, op.path, op.body, bearer(memberToken)), TypeValidationFailed)
		requireProblem(t, do(t, h, op.method, op.path, op.body, with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
		requireProblem(t, do(t, h, op.method, op.path, op.body, with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	}
	// Demo mode refuses the create to a non-admin and leaves the reads.
	demo := requestDeps(fixtureRequests())
	demo.DemoSettings = fakeSettings{demo: true}
	hd := newTestHandler(t, demo)
	requireProblem(t, do(t, hd, http.MethodPost, "/api/v2/requests", ops[0].body, requestOwner), TypePermissionDenied)
	if rec := do(t, hd, http.MethodGet, "/api/v2/requests/mine", "", requestOwner); rec.Code != 200 {
		t.Fatalf("demo read: %d %s", rec.Code, rec.Body.String())
	}
	// A disabled feature is a capability problem on every operation; a
	// missing service fails closed.
	disabled := fixtureRequests()
	disabled.err = mediarequests.ErrRequestsDisabled
	hx := newTestHandler(t, requestDeps(disabled))
	unwired := requestDeps(nil)
	unwired.Requests = nil
	hu := newTestHandler(t, unwired)
	for _, op := range ops {
		requireProblem(t, do(t, hx, op.method, op.path, op.body, requestOwner), TypeCapabilityDisabled)
		requireProblem(t, do(t, hu, op.method, op.path, op.body, requestOwner), TypeDependencyUnavailable)
	}
}

func TestFollowRequestMedia(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))

	rec := do(t, h, http.MethodPut, "/api/v2/requests/follows/movie/949", "", requestOwner)
	var got RequestMediaState
	decodeBody(t, rec.Body, &got)
	if rec.Code != http.StatusOK || !got.Following || got.Status != "pending" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if svc.lastCall != "follow" || svc.lastArgs[0] != mediarequests.MediaTypeMovie || svc.lastArgs[1] != 949 || svc.lastViewer.ProfileID != "p-owner" {
		t.Fatalf("call = %s %v viewer = %+v", svc.lastCall, svc.lastArgs, svc.lastViewer)
	}

	rec = do(t, h, http.MethodDelete, "/api/v2/requests/follows/series/1399", "", requestOwner)
	if rec.Code != http.StatusNoContent || svc.lastCall != "unfollow" || svc.lastArgs[0] != mediarequests.MediaTypeSeries {
		t.Fatalf("%d %s call = %s %v", rec.Code, rec.Body.String(), svc.lastCall, svc.lastArgs)
	}

	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/requests/follows/tv/949", "", requestOwner), TypeValidationFailed)
	svc.err = mediarequests.ErrNotRequested
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/requests/follows/movie/949", "", requestOwner), TypeConflict)
	svc.err = mediarequests.ErrAlreadyAvailable
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/requests/follows/movie/949", "", requestOwner), TypeConflict)
}

func TestCreateSeriesRequestPassesSeasons(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))
	rec := do(t, h, http.MethodPost, "/api/v2/requests", `{"media_type":"series","tmdb_id":95396,"title":"Severance","seasons":[2,3]}`, requestOwner)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !slices.Equal(svc.lastCreate.Seasons, []int{2, 3}) {
		t.Fatalf("seasons passed = %v, want [2 3]", svc.lastCreate.Seasons)
	}
	var got struct {
		Seasons        []int `json:"seasons"`
		SeasonProgress []any `json:"season_progress"`
	}
	decodeBody(t, rec.Body, &got)
	if got.Seasons == nil || got.SeasonProgress == nil {
		t.Fatalf("seasons fields must be arrays, never null: %s", rec.Body.String())
	}
}

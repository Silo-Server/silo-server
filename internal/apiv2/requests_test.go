package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// fakeRequests is a MediaRequestService over a fixed request list and
// canned provider answers; it records the last call it saw.
type fakeRequests struct {
	requests []*mediarequests.Request
	err      error

	lastViewer mediarequests.Viewer
	lastFilter mediarequests.ListFilter
	lastCreate mediarequests.CreateRequestInput
	lastCall   string
	lastArgs   []any
}

func fixtureMediaRequest(id string, tmdbID int) *mediarequests.Request {
	year := 1995
	approved := fixedTime()
	return &mediarequests.Request{
		ID: id, Provider: "tmdb", MediaType: mediarequests.MediaTypeMovie, TMDBID: tmdbID, Title: "Heat", Year: &year,
		Status: mediarequests.StatusApproved, Outcome: mediarequests.OutcomeActive, RequestedByUserID: 1, RequestedByProfileID: "p-owner",
		IntegrationKind: "radarr", Targets: []mediarequests.Target{{ID: 42, RequestID: id, Quality: mediarequests.Quality1080p, Status: mediarequests.StatusQueued, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}},
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
		LibraryContentID: "movie:heat-1995", Request: mediarequests.RequestState{Reason: "already_available"},
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
	return &fakeRequests{requests: []*mediarequests.Request{fixtureMediaRequest("r-1", 949), pending, other}}
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

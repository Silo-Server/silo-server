package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
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

func (f *fakeRequests) DiscoverRow(_ context.Context, viewer mediarequests.Viewer, rowID string, page int) (*mediarequests.DiscoverySection, error) {
	if err := f.record(viewer, "discover", rowID, page); err != nil {
		return nil, err
	}
	return &mediarequests.DiscoverySection{Key: rowID, Title: "Trending Movies", Page: page, TotalPages: 500, TotalResults: 10000, Results: []mediarequests.MediaResult{fixtureResult(949)}, NextPage: page + 2}, nil
}

func (f *fakeRequests) DiscoverRows(_ context.Context, viewer mediarequests.Viewer) (*mediarequests.DiscoverLayoutPage, error) {
	if err := f.record(viewer, "discoverAll"); err != nil {
		return nil, err
	}
	return &mediarequests.DiscoverLayoutPage{
		Order: []mediarequests.DiscoverRowRef{
			{Key: "trending_movies", Type: "tmdb_chart", Title: "Trending Movies"},
			{Key: "studios", Type: "studios", Title: "Studios"},
		},
		Sections: []mediarequests.DiscoverySection{{Key: "trending_movies", Title: "Trending Movies", Page: 1, TotalPages: 500, TotalResults: 10000, Results: []mediarequests.MediaResult{fixtureResult(949)}}},
	}, nil
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

func TestListMyRequestsOmitsZeroDownloadEstimate(t *testing.T) {
	svc := fixtureRequests()
	svc.requests = svc.requests[:1]
	zero := time.Time{}
	svc.requests[0].Targets[0].Download.EstimatedCompletion = &zero

	rec := do(t, NewHandler(requestDeps(svc)), http.MethodGet, Prefix+"/requests/mine", "", requestOwner)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var body MediaRequestCollection
	decodeBody(t, rec.Body, &body)
	if len(body.Items) != 1 || body.Items[0].Download == nil || body.Items[0].Download.EstimatedCompletionAt != nil {
		t.Fatalf("items = %+v, want progress without estimated_completion_at", body.Items)
	}
	if len(body.Items[0].Targets) != 1 || body.Items[0].Targets[0].Download == nil || body.Items[0].Targets[0].Download.EstimatedCompletionAt != nil {
		t.Fatalf("targets = %+v, want progress without estimated_completion_at", body.Items[0].Targets)
	}
}

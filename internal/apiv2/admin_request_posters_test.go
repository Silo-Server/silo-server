package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// posteredRequests gives r-1 a TMDB poster and r-2 a poster path in another
// shape, and reads posters the way the request service does.
type posteredRequests struct{ monitoredRequestQueue }

func (f posteredRequests) ListAdmin(ctx context.Context, v mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error) {
	rows, err := f.monitoredRequestQueue.ListAdmin(ctx, v, filter)
	for _, r := range rows {
		switch r.ID {
		case "r-1":
			r.PosterPath = "/heat.jpg"
		case "r-2":
			r.PosterPath = "https://example.test/heat.jpg"
		}
	}
	return rows, err
}

func (posteredRequests) AdminRequestPosterURL(_ context.Context, v mediarequests.Viewer, id string) (string, error) {
	if !v.IsAdmin {
		return "", mediarequests.ErrForbidden
	}
	if id != "r-1" {
		return "", mediarequests.ErrNotFound
	}
	return "https://image.tmdb.org/t/p/w500/heat.jpg", nil
}

// A monitoring key reads a request's poster_url and follows it with the same
// key. Only a request with a TMDB poster gets one.
func TestAdminRequestPosterFollowsFromTheList(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.AdminRequests = posteredRequests{monitoredRequestQueue{fixtureAdminRequests()}}
	fetcher := &posterFetcherFake{}
	deps.RemotePosters = fetcher
	users := map[int]*models.User{1: {ID: 1, Role: "user", Enabled: true}, 2: {ID: 2, Role: "admin", Enabled: true}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_monitor":        {ID: 1, UserID: 2, Scopes: []string{auth.ScopeAdminMonitorRead}},
		"sa_member_monitor": {ID: 2, UserID: 1, Scopes: []string{auth.ScopeAdminMonitorRead}},
	}}, fakeUsers{users: users})
	h := NewHandler(deps)

	var page struct {
		Items []MediaRequest `json:"items"`
	}
	list := do(t, h, "GET", Prefix+"/admin/requests", "", bearer("sa_monitor"))
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || list.Code != http.StatusOK {
		t.Fatal(list.Code, list.Body.String())
	}
	posters := map[string]string{}
	for _, item := range page.Items {
		posters[string(item.ID)] = item.PosterURL
	}
	if posters["r-1"] != Prefix+"/admin/requests/r-1/poster" || posters["r-2"] != "" || posters["r-3"] != "" {
		t.Fatalf("poster_url by request: %v", posters)
	}

	rec := do(t, h, "GET", posters["r-1"], "", bearer("sa_monitor"))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || len(fetcher.urls) != 1 || fetcher.urls[0] != "https://image.tmdb.org/t/p/w500/heat.jpg" {
		t.Fatalf("poster: %d %v fetched %v", rec.Code, rec.Header(), fetcher.urls)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/requests/r-2/poster", "", bearer("sa_monitor")), TypeNotFound)
	requireProblem(t, do(t, h, "GET", posters["r-1"], "", bearer("sa_member_monitor")), TypePermissionDenied)

	caps := do(t, h, "GET", Prefix+"/admin/requests/capabilities", "", bearer("sa_monitor"))
	var body AdminRequestCapabilitiesOutputBody
	if err := json.Unmarshal(caps.Body.Bytes(), &body); err != nil || !body.RequestPosters {
		t.Fatal(caps.Code, caps.Body.String())
	}
}

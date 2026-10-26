package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type identityCatalog struct {
	*fakeCatalog
	request catalogpkg.IdentityRequest
	viewer  handlers.ItemViewer
}

func (f *identityCatalog) ResolveIdentities(_ context.Context, v handlers.ItemViewer, req catalogpkg.IdentityRequest) (handlers.CatalogIdentityView, error) {
	f.request, f.viewer = req, v
	return handlers.CatalogIdentityView{Matches: []handlers.CatalogIdentityMatchView{{Seed: 0, Item: fakeListingCard("native-copy-a")}}, HasMore: true, Next: catalogpkg.IdentityPosition{Seed: 0, ContentID: "native-copy-a"}}, nil
}
func TestResolveCatalogIdentities(t *testing.T) {
	deps, base := catalogDeps(t)
	service := &identityCatalog{fakeCatalog: base}
	deps.CatalogBrowse = service
	h := newTestHandler(t, deps)
	body := `{"seeds":[{"type":"movie","tmdb_id":"101","imdb_id":"tt101"}],"limit":1}`
	rec := do(t, h, http.MethodPost, "/api/v2/catalog/resolve", body, viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var result CatalogIdentityCollection
	decodeJSON(t, rec.Body, &result)
	if len(result.Matches) != 1 || result.Matches[0].Seed != 0 || result.Matches[0].Item.ContentID != "native-copy-a" || result.Complete || result.NextCursor == "" {
		t.Fatalf("response = %+v", result)
	}
	if service.viewer.ProfileID != "p-owner" || len(service.viewer.Access.AllowedLibraryIDs) != 2 || service.request.Limit != 1 {
		t.Fatalf("viewer/request = %+v %+v", service.viewer, service.request)
	}
	next := `{"seeds":[{"type":"movie","tmdb_id":"101","imdb_id":"tt101"}],"limit":1,"cursor":"` + result.NextCursor + `"}`
	rec = do(t, h, http.MethodPost, "/api/v2/catalog/resolve", next, viewerHeaders())
	if rec.Code != 200 || service.request.After.ContentID != "native-copy-a" {
		t.Fatalf("continuation = %d %+v", rec.Code, service.request)
	}
	changed := `{"seeds":[{"type":"series","tmdb_id":"101","imdb_id":"tt101"}],"limit":1,"cursor":"` + result.NextCursor + `"}`
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/resolve", changed, viewerHeaders()), TypeInvalidCursor)
	for _, changed := range []struct{ path, body string }{
		{"/api/v2/catalog/resolve", strings.Replace(next, `"limit":1`, `"limit":2`, 1)},
		{"/api/v2/catalog/resolve?image_size=small", next},
	} {
		requireProblem(t, do(t, h, http.MethodPost, changed.path, changed.body, viewerHeaders()), TypeInvalidCursor)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/resolve", body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/resolve", `{"seeds":[{"type":"movie"}]}`, viewerHeaders()), TypeValidationFailed)
	for _, invalid := range []string{
		`{"seeds":[{"type":"episode","tmdb_id":"101"}]}`,
		`{"seeds":[{"type":"movie","tmdb_id":"101"}],"limit":101}`,
		`{"seeds":[` + strings.Repeat(`{"type":"movie","tmdb_id":"101"},`, 48) + `{"type":"movie","tmdb_id":"101"}]}`,
	} {
		requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/resolve", invalid, viewerHeaders()), TypeValidationFailed)
	}
	caps := do(t, h, http.MethodGet, "/api/v2/catalog/search/capabilities", "", viewerHeaders())
	var capability CatalogSearchCapabilities
	decodeJSON(t, caps.Body, &capability)
	if caps.Code != 200 || !capability.IdentityResolution || capability.MaxIdentityBatch != 48 {
		t.Fatalf("capabilities = %d %+v", caps.Code, capability)
	}
	deps.CatalogBrowse = base
	fallback := newTestHandler(t, deps)
	caps = do(t, fallback, http.MethodGet, "/api/v2/catalog/search/capabilities", "", viewerHeaders())
	capability = CatalogSearchCapabilities{}
	decodeJSON(t, caps.Body, &capability)
	if capability.IdentityResolution || capability.MaxIdentityBatch != 0 {
		t.Fatalf("unsupported capabilities = %+v", capability)
	}

}

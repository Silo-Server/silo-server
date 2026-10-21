package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// fakeRouteAdmin adds route administration to the admin request fake.
type fakeRouteAdmin struct {
	*fakeAdminRequests
	routes   map[string]mediarequests.Route
	order    []string
	preview  mediarequests.RoutePreview
	lastSave mediarequests.Route
	searched string
	routing  mediarequests.RoutingOverview
}

func fixtureRouteAdmin() *fakeRouteAdmin {
	fallback := mediarequests.Route{ID: "fallback-movie", MediaType: mediarequests.MediaTypeMovie, Position: 1000, Name: "Everything else",
		Enabled: true, IsFallback: true, HD: mediarequests.RouteDestination{IntegrationID: "radarr"}, Revision: 4}
	anime := mediarequests.Route{ID: "r-anime", MediaType: mediarequests.MediaTypeMovie, Position: 0, Name: "Anime", Enabled: true,
		Conditions: mediarequests.RouteConditions{Anime: new(true)},
		HD:         mediarequests.RouteDestination{IntegrationID: "radarr-anime", Overrides: map[string]any{"root_folder": "/anime"}}, Revision: 5}
	return &fakeRouteAdmin{
		fakeAdminRequests: fixtureAdminRequests(),
		routes:            map[string]mediarequests.Route{fallback.ID: fallback, anime.ID: anime},
		order:             []string{anime.ID, fallback.ID},
	}
}

func (f *fakeRouteAdmin) ListRoutesAdmin(_ context.Context, v mediarequests.Viewer) ([]mediarequests.Route, error) {
	f.viewer = v
	out := make([]mediarequests.Route, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.routes[id])
	}
	return out, nil
}

func (f *fakeRouteAdmin) GetRoute(_ context.Context, _ mediarequests.Viewer, id string) (*mediarequests.Route, error) {
	r, ok := f.routes[id]
	if !ok {
		return nil, mediarequests.ErrNotFound
	}
	return &r, nil
}

func (f *fakeRouteAdmin) CreateRoute(_ context.Context, _ mediarequests.Viewer, r mediarequests.Route) (*mediarequests.Route, error) {
	f.writes++
	r.ID, r.Revision = "r-new", 1
	f.routes[r.ID] = r
	return &r, nil
}

func (f *fakeRouteAdmin) UpdateRouteConditional(_ context.Context, _ mediarequests.Viewer, r mediarequests.Route, expected int64) (*mediarequests.Route, error) {
	current := f.routes[r.ID]
	if expected != -1 && expected != current.Revision {
		return nil, mediarequests.ErrStaleRevision
	}
	f.writes++
	f.lastSave = r
	r.Revision = current.Revision + 1
	r.IsFallback, r.MediaType, r.Position = current.IsFallback, current.MediaType, current.Position
	f.routes[r.ID] = r
	return &r, nil
}

func (f *fakeRouteAdmin) DeleteRouteConditional(_ context.Context, _ mediarequests.Viewer, id string, expected int64) error {
	if f.routes[id].IsFallback {
		return &mediarequests.ValidationError{FormError: "The default destination cannot be deleted; clear its servers instead."}
	}
	if expected != f.routes[id].Revision {
		return mediarequests.ErrStaleRevision
	}
	f.writes++
	delete(f.routes, id)
	return nil
}

func (f *fakeRouteAdmin) ReorderRoutes(ctx context.Context, v mediarequests.Viewer, _ mediarequests.MediaType, ids []string) ([]mediarequests.Route, error) {
	f.order = append(append([]string(nil), ids...), "fallback-movie")
	return f.ListRoutesAdmin(ctx, v)
}

func (f *fakeRouteAdmin) PreviewRoute(context.Context, mediarequests.Viewer, mediarequests.MediaType, int, int) (*mediarequests.RoutePreview, error) {
	return &f.preview, nil
}

func (f *fakeRouteAdmin) SearchRouteTitles(_ context.Context, _ mediarequests.Viewer, mediaType mediarequests.MediaType, q string) ([]tmdb.MediaResult, error) {
	f.searched = q
	return []tmdb.MediaResult{{ID: 129, MediaType: string(mediaType), Title: "Spirited Away", Year: 2001}}, nil
}

func (f *fakeRouteAdmin) GetRoutingOverview(context.Context, mediarequests.Viewer) (*mediarequests.RoutingOverview, error) {
	o := f.routing
	return &o, nil
}

func (f *fakeRouteAdmin) UpdateRoutingModeConditional(_ context.Context, _ mediarequests.Viewer, mode mediarequests.RoutingMode, expected int64) (*mediarequests.RoutingOverview, error) {
	if expected != -1 && expected != f.routing.Revision {
		return nil, mediarequests.ErrStaleRevision
	}
	if mode == mediarequests.RoutingStandard && f.routing.StandardBlocker != "" {
		return nil, &mediarequests.ValidationError{FieldErrors: map[string]string{"mode": f.routing.StandardBlocker}}
	}
	f.writes++
	f.routing.Mode, f.routing.Revision = mode, f.routing.Revision+1
	o := f.routing
	return &o, nil
}

func routeAdminHandler(f *fakeRouteAdmin) http.Handler {
	deps := requestDeps(fixtureRequests())
	deps.AdminRequests = f
	return NewHandler(deps)
}

const routeBody = `{"name":"Anime","enabled":true,"conditions":{"anime":true,"original_languages":["ja"]},"hd":{"integration_id":"radarr-anime","overrides":{"root_folder":"/anime"}},"uhd":{},"skip_uhd":true}`

func TestAdminRequestRoutesListAndGuards(t *testing.T) {
	f := fixtureRouteAdmin()
	h := routeAdminHandler(f)
	base := Prefix + "/admin/request-routes"

	requireProblem(t, do(t, h, http.MethodGet, base, "", requestOwner), TypePermissionDenied)
	var list struct {
		Items []AdminRequestRoute `json:"items"`
	}
	rec := do(t, h, http.MethodGet, base, "", actingRequestAdmin)
	decodeBody(t, rec.Body, &list)
	if rec.Code != 200 || len(list.Items) != 2 || list.Items[0].Name != "Anime" || !list.Items[1].IsFallback ||
		list.Items[0].HD.Overrides["root_folder"] != "/anime" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	read := do(t, h, http.MethodGet, base+"/r-anime", "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" {
		t.Fatal(read.Code, read.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPut, base+"/r-anime", routeBody, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, base+"/r-anime", routeBody, with(actingRequestAdmin, "If-Match", `"stale"`)), TypePreconditionFailed)
	if f.writes != 0 {
		t.Fatal("a guarded write took effect")
	}
	saved := do(t, h, http.MethodPut, base+"/r-anime", routeBody, with(actingRequestAdmin, "If-Match", tag))
	if saved.Code != 200 || saved.Header().Get("ETag") == tag || !f.lastSave.SkipUHD ||
		f.lastSave.Conditions.OriginalLanguages[0] != "ja" || f.lastSave.HD.IntegrationID != "radarr-anime" {
		t.Fatalf("%d %s saved=%+v", saved.Code, saved.Body.String(), f.lastSave)
	}

	fallback := do(t, h, http.MethodGet, base+"/fallback-movie", "", actingRequestAdmin)
	requireProblem(t, do(t, h, http.MethodDelete, base+"/fallback-movie", "", with(actingRequestAdmin, "If-Match", fallback.Header().Get("ETag"))), TypeValidationFailed)
	current := do(t, h, http.MethodGet, base+"/r-anime", "", actingRequestAdmin)
	deleted := do(t, h, http.MethodDelete, base+"/r-anime", "", with(actingRequestAdmin, "If-Match", current.Header().Get("ETag")))
	if deleted.Code != http.StatusNoContent {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
}

func TestAdminRequestRoutesCreateReorderPreview(t *testing.T) {
	f := fixtureRouteAdmin()
	h := routeAdminHandler(f)
	base := Prefix + "/admin/request-routes"

	created := do(t, h, http.MethodPost, base, `{"media_type":"movie","name":"80s","enabled":true,"conditions":{"year_from":1980,"year_to":1989},"hd":{"integration_id":"radarr-retro"},"uhd":{},"skip_uhd":false}`, actingRequestAdmin)
	if created.Code != http.StatusCreated || created.Header().Get("Location") != base+"/r-new" || created.Header().Get("ETag") == "" {
		t.Fatal(created.Code, created.Body.String())
	}
	if r := f.routes["r-new"]; r.MediaType != mediarequests.MediaTypeMovie || r.Conditions.YearFrom != 1980 {
		t.Fatalf("created = %+v", r)
	}

	var reordered struct {
		Items []AdminRequestRoute `json:"items"`
	}
	rec := do(t, h, http.MethodPost, base+"/order", `{"media_type":"movie","ids":["r-new","r-anime"]}`, actingRequestAdmin)
	decodeBody(t, rec.Body, &reordered)
	if rec.Code != 200 || len(reordered.Items) != 3 || reordered.Items[0].ID != "r-new" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	f.preview = mediarequests.RoutePreview{
		Facts: mediarequests.RoutingFacts{Anime: true, OriginalLanguage: "ja", Year: 2001},
		Tiers: []mediarequests.RoutePreviewTier{
			{Quality: mediarequests.Quality1080p, RouteID: "r-anime", RouteName: "Anime", IntegrationID: "radarr-anime", IntegrationName: "Radarr Anime"},
			{Quality: mediarequests.Quality2160p, Reason: "No rule sends 4K for this title."},
		},
		Rules: []mediarequests.RouteTrace{
			{Route: mediarequests.Route{ID: "r-kids", Name: "Kids", Enabled: true}, Unmet: []string{"genre_ids", "max_content_rating"},
				Steps: map[mediarequests.Quality]mediarequests.RouteStep{mediarequests.Quality1080p: mediarequests.RouteStepNoMatch, mediarequests.Quality2160p: mediarequests.RouteStepNoMatch}},
			{Route: mediarequests.Route{ID: "r-anime", Name: "Anime", Enabled: true},
				Steps: map[mediarequests.Quality]mediarequests.RouteStep{mediarequests.Quality1080p: mediarequests.RouteStepSends, mediarequests.Quality2160p: mediarequests.RouteStepPasses}},
		},
	}
	f.preview.Facts.ContentRating = new("TV-14")
	var preview struct {
		Facts AdminRequestRouteFacts         `json:"facts"`
		Tiers []AdminRequestRoutePreviewTier `json:"tiers"`
		Rules []AdminRequestRoutePreviewRule `json:"rules"`
	}
	rec = do(t, h, http.MethodPost, base+"/preview", `{"media_type":"movie","tmdb_id":129,"requester_user_id":"7"}`, actingRequestAdmin)
	decodeBody(t, rec.Body, &preview)
	if rec.Code != 200 || !preview.Facts.Anime || preview.Facts.GenreIDs == nil || len(preview.Tiers) != 2 ||
		preview.Tiers[0].IntegrationName != "Radarr Anime" || preview.Tiers[1].Note == "" || preview.Facts.ContentRating != "TV-14" ||
		len(preview.Rules) != 2 || len(preview.Rules[0].Unmet) != 2 || preview.Rules[1].HD != "sends" || preview.Rules[1].UHD != "passes" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	var titles struct {
		Items []AdminRequestRouteTitle `json:"items"`
	}
	requireProblem(t, do(t, h, http.MethodGet, base+"/titles?media_type=movie&q=spirited", "", requestOwner), TypePermissionDenied)
	rec = do(t, h, http.MethodGet, base+"/titles?media_type=movie&q=spirited", "", actingRequestAdmin)
	decodeBody(t, rec.Body, &titles)
	if rec.Code != 200 || len(titles.Items) != 1 || titles.Items[0].TMDBID != 129 || f.searched != "spirited" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, base+"/preview", `{"media_type":"movie","tmdb_id":129,"requester_user_id":"x"}`, actingRequestAdmin), TypeValidationFailed)
}

func TestAdminRequestRoutingMode(t *testing.T) {
	f := fixtureRouteAdmin()
	f.routing = mediarequests.RoutingOverview{
		RoutingSettings: mediarequests.RoutingSettings{Mode: mediarequests.RoutingStandard, Revision: 3},
		Standard:        []mediarequests.StandardDestination{{MediaType: mediarequests.MediaTypeMovie, HDIntegrationID: "radarr", UHDIntegrationID: "radarr-4k"}},
	}
	h := routeAdminHandler(f)
	path := Prefix + "/admin/request-routing"

	requireProblem(t, do(t, h, http.MethodGet, path, "", requestOwner), TypePermissionDenied)
	var got AdminRequestRouting
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	decodeBody(t, read.Body, &got)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" || got.Mode != "standard" || len(got.Standard) != 1 ||
		got.Standard[0].UHDIntegrationID != "radarr-4k" || got.StandardUnavailableReason != "" {
		t.Fatalf("%d %s", read.Code, read.Body.String())
	}

	requireProblem(t, do(t, h, http.MethodPut, path, `{"mode":"advanced"}`, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path, `{"mode":"advanced"}`, with(actingRequestAdmin, "If-Match", `"stale"`)), TypePreconditionFailed)
	requireProblem(t, do(t, h, http.MethodPut, path, `{"mode":"sideways"}`, with(actingRequestAdmin, "If-Match", tag)), TypeValidationFailed)
	if f.writes != 0 {
		t.Fatal("a refused switch took effect")
	}
	saved := do(t, h, http.MethodPut, path, `{"mode":"advanced"}`, with(actingRequestAdmin, "If-Match", tag))
	decodeBody(t, saved.Body, &got)
	if saved.Code != 200 || got.Mode != "advanced" || saved.Header().Get("ETag") == tag {
		t.Fatalf("%d %s", saved.Code, saved.Body.String())
	}

	f.routing.Standard, f.routing.StandardBlocker = nil, "Movies can go to more than one server (A, B)."
	current := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	decodeBody(t, current.Body, &got)
	if got.Standard == nil || len(got.Standard) != 0 || got.StandardUnavailableReason == "" {
		t.Fatalf("blocked overview = %s", current.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPut, path, `{"mode":"standard"}`, with(actingRequestAdmin, "If-Match", current.Header().Get("ETag"))), TypeValidationFailed)
}

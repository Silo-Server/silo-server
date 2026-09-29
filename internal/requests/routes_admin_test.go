package requests

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// routeAdminService wires a Service over the lifecycle test schema, with the
// route table copied in and Radarr/Sonarr servers seeded.
func routeAdminService(t *testing.T) (*Service, *Repository) {
	t.Helper()
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `CREATE TABLE request_routes (LIKE public.request_routes INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER route_revision BEFORE INSERT OR UPDATE ON request_routes
		FOR EACH ROW EXECUTE FUNCTION public.advance_request_editor_revision()`); err != nil {
		t.Fatal(err)
	}
	for _, seed := range [][2]string{
		{"radarr", `{"service_kind":"radarr"}`}, {"radarr-anime", `{"service_kind":"radarr"}`}, {"sonarr", `{"service_kind":"sonarr"}`},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO request_integrations (id, name, enabled, capability_id, plugin_config) VALUES ($1, $1, true, 'arr', $2::jsonb)`, seed[0], seed[1]); err != nil {
			t.Fatal(err)
		}
	}
	return NewService(repo, &fakeTMDBClient{}, &fakePresence{}), repo
}

var routeAdmin = Viewer{UserID: 1, IsAdmin: true}

func fieldErrors(t *testing.T, err error) map[string]string {
	t.Helper()
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	return verr.FieldErrors
}

func TestRouteAdministrationDatabase(t *testing.T) {
	svc, repo := routeAdminService(t)
	ctx := t.Context()

	// Until the fallback has an HD server there is nowhere for titles no rule
	// matches to go, so rules wait for it.
	_, err := svc.CreateRoute(ctx, routeAdmin, Route{
		MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "radarr-anime"},
	})
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.FormError, "Choose where everything else goes") {
		t.Fatalf("rule before the fallback: err = %v, want the default server asked for", err)
	}
	unsaved, err := svc.GetRoute(ctx, routeAdmin, FallbackRouteID(MediaTypeMovie))
	if err != nil || !unsaved.IsFallback || unsaved.Revision != 0 {
		t.Fatalf("unsaved fallback = %+v, %v; want a revision-zero fallback", unsaved, err)
	}

	// The fallback is created on its first save and takes no conditions.
	fallback, err := svc.UpdateRouteConditional(ctx, routeAdmin, Route{
		ID: FallbackRouteID(MediaTypeMovie), HD: RouteDestination{IntegrationID: "radarr"},
	}, 0)
	if err != nil {
		t.Fatalf("save fallback: %v", err)
	}
	if !fallback.IsFallback || fallback.MediaType != MediaTypeMovie || fallback.Name != "Everything else" || !fallback.Enabled {
		t.Fatalf("fallback = %+v", fallback)
	}

	anime, err := svc.CreateRoute(ctx, routeAdmin, Route{
		MediaType: MediaTypeMovie, Name: " Anime ", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true), OriginalLanguages: []string{"JA", "ja"}},
		HD:         RouteDestination{IntegrationID: "radarr-anime", Overrides: map[string]any{"root_folder": "/anime"}},
	})
	if err != nil {
		t.Fatalf("create anime rule: %v", err)
	}
	eighties, err := svc.CreateRoute(ctx, routeAdmin, Route{
		MediaType: MediaTypeMovie, Name: "80s", Enabled: true,
		Conditions: RouteConditions{YearFrom: 1980, YearTo: 1989}, SkipUHD: true,
	})
	if err != nil {
		t.Fatalf("create 80s rule: %v", err)
	}
	if anime.Name != "Anime" || !slices.Equal(anime.Conditions.OriginalLanguages, []string{"ja"}) || eighties.Position <= anime.Position {
		t.Fatalf("anime = %+v 80s = %+v, want normalized and appended in order", anime, eighties)
	}

	// A stale editor loses.
	stale := *anime
	stale.Name = "Anime (old tab)"
	if _, err := svc.UpdateRouteConditional(ctx, routeAdmin, stale, anime.Revision-1); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale update: err = %v, want ErrStaleRevision", err)
	}
	renamed := *anime
	renamed.Name = "Anime and donghua"
	updated, err := svc.UpdateRouteConditional(ctx, routeAdmin, renamed, anime.Revision)
	if err != nil || updated.Name != "Anime and donghua" || updated.Position != anime.Position {
		t.Fatalf("update = %+v, %v; want renamed in place", updated, err)
	}

	ordered, err := svc.ReorderRoutes(ctx, routeAdmin, MediaTypeMovie, []string{eighties.ID, anime.ID})
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	var names []string
	for _, route := range ordered {
		names = append(names, route.Name)
	}
	if !slices.Equal(names, []string{"80s", "Anime and donghua", "Everything else"}) {
		t.Fatalf("order = %v, want the new order with the fallback last", names)
	}
	if _, err := svc.ReorderRoutes(ctx, routeAdmin, MediaTypeMovie, []string{anime.ID}); err == nil {
		t.Fatal("a reorder missing a rule was accepted")
	}

	if err := svc.DeleteRouteConditional(ctx, routeAdmin, fallback.ID, fallback.Revision); err == nil {
		t.Fatal("deleting the fallback was accepted")
	}
	// Reordering is an edit: it moves the rules to new revisions.
	if err := svc.DeleteRouteConditional(ctx, routeAdmin, eighties.ID, eighties.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("delete with the pre-reorder revision: err = %v, want ErrStaleRevision", err)
	}
	if err := svc.DeleteRouteConditional(ctx, routeAdmin, eighties.ID, ordered[0].Revision); err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	if _, err := repo.GetRoute(ctx, eighties.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted rule: err = %v, want ErrNotFound", err)
	}
}

func TestRouteValidationDatabase(t *testing.T) {
	svc, _ := routeAdminService(t)
	ctx := t.Context()
	for _, tc := range []struct {
		name  string
		route Route
		field string
	}{
		{"no name", Route{MediaType: MediaTypeMovie, Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "radarr"}}, "name"},
		{"no condition", Route{MediaType: MediaTypeMovie, Name: "All", HD: RouteDestination{IntegrationID: "radarr"}}, "conditions"},
		{"no effect", Route{MediaType: MediaTypeMovie, Name: "Nothing", Conditions: RouteConditions{Anime: boolPtr(true)}}, "hd"},
		{"wrong kind", Route{MediaType: MediaTypeMovie, Name: "Wrong", Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "sonarr"}}, "hd.integration_id"},
		{"unknown server", Route{MediaType: MediaTypeMovie, Name: "Gone", Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "nope"}}, "hd.integration_id"},
		{"routing-owned key", Route{MediaType: MediaTypeMovie, Name: "Sneaky", Conditions: RouteConditions{Anime: boolPtr(true)},
			HD: RouteDestination{IntegrationID: "radarr", Overrides: map[string]any{"is_default": true}}}, "hd.overrides.is_default"},
		{"backwards years", Route{MediaType: MediaTypeMovie, Name: "Years", Conditions: RouteConditions{YearFrom: 1990, YearTo: 1980}, HD: RouteDestination{IntegrationID: "radarr"}}, "conditions.year_to"},
		{"bad language", Route{MediaType: MediaTypeMovie, Name: "Lang", Conditions: RouteConditions{OriginalLanguages: []string{"japanese"}}, HD: RouteDestination{IntegrationID: "radarr"}}, "conditions.original_languages"},
		{"skip and send 4K", Route{MediaType: MediaTypeMovie, Name: "Both", Conditions: RouteConditions{Anime: boolPtr(true)}, SkipUHD: true, UHD: RouteDestination{IntegrationID: "radarr"}}, "uhd"},
	} {
		_, err := svc.CreateRoute(ctx, routeAdmin, tc.route)
		if fields := fieldErrors(t, err); fields[tc.field] == "" {
			t.Errorf("%s: field errors = %v, want %s", tc.name, fields, tc.field)
		}
	}
	_, err := svc.UpdateRouteConditional(ctx, routeAdmin, Route{
		ID: FallbackRouteID(MediaTypeSeries), Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "sonarr"},
	}, 0)
	if fields := fieldErrors(t, err); fields["conditions"] == "" {
		t.Fatalf("fallback with conditions: field errors = %v", fields)
	}
}

func TestRouteAdministrationRequiresAdmin(t *testing.T) {
	svc := newTestService(newFakeStore())
	member := Viewer{UserID: 2}
	if _, err := svc.ListRoutesAdmin(context.Background(), member); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list: err = %v", err)
	}
	if _, err := svc.CreateRoute(context.Background(), member, Route{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create: err = %v", err)
	}
	if _, err := svc.PreviewRoute(context.Background(), member, MediaTypeMovie, 1, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("preview: err = %v", err)
	}
}

func TestPreviewRoute(t *testing.T) {
	store := routingStore(RoutingFacts{})
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{ID: 129, KeywordIDs: []int{210024}}})

	preview, err := svc.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 129, 7)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.Facts.Anime || len(preview.Tiers) != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	hd, uhd := preview.Tiers[0], preview.Tiers[1]
	if hd.RouteName != "Anime" || hd.IntegrationName != "radarr-anime" || uhd.RouteName != "Everything else" {
		t.Fatalf("tiers = %+v", preview.Tiers)
	}
}

func TestRouteAdministrationGuardsDatabase(t *testing.T) {
	svc, repo := routeAdminService(t)
	ctx := t.Context()

	// A fallback without an HD server would fail every unmatched title.
	_, err := svc.UpdateRouteConditional(ctx, routeAdmin, Route{ID: FallbackRouteID(MediaTypeMovie), UHD: RouteDestination{IntegrationID: "radarr"}}, 0)
	if fields := fieldErrors(t, err); fields["hd.integration_id"] == "" {
		t.Fatalf("fallback without HD: field errors = %v", fields)
	}

	// Two first saves of the fallback: the second editor read revision zero
	// too, and must lose instead of overwriting.
	first, err := repo.SaveRouteConditional(ctx, Route{ID: FallbackRouteID(MediaTypeMovie), MediaType: MediaTypeMovie, Position: 1000,
		Name: "Everything else", Enabled: true, IsFallback: true, HD: RouteDestination{IntegrationID: "radarr"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveRouteConditional(ctx, Route{ID: first.ID, MediaType: MediaTypeMovie, Position: 1000,
		Name: "Other editor", Enabled: true, IsFallback: true, HD: RouteDestination{IntegrationID: "radarr-anime"}}, 0); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("second first save: err = %v, want ErrStaleRevision", err)
	}

	// A rule deleted under an editor does not come back on save.
	rule, err := svc.CreateRoute(ctx, routeAdmin, Route{MediaType: MediaTypeMovie, Name: "アニメとドンファのための特別なルール、とても長い名前でも大丈夫", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "radarr-anime"}})
	if err != nil {
		t.Fatalf("create a rule with a long non-Latin name: %v", err)
	}
	if err := svc.DeleteRouteConditional(ctx, routeAdmin, rule.ID, rule.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveRouteConditional(ctx, *rule, rule.Revision); !errors.Is(err, ErrStaleRevision) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("save a deleted rule: err = %v, want it refused", err)
	}
	if _, err := repo.GetRoute(ctx, rule.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted rule came back: %v", err)
	}
}

func TestPreviewRouteTellsMissingFromUnreachable(t *testing.T) {
	store := routingStore(RoutingFacts{})
	missing := newTestServiceWithTMDB(store, &fakeTMDBClient{detailErr: fmt.Errorf("lookup: %w", tmdb.ErrNotFound)})
	if _, err := missing.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 1, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing title: err = %v, want ErrNotFound", err)
	}
	down := newTestServiceWithTMDB(store, &fakeTMDBClient{detailErr: errors.New("tmdb: server error 503 after 3 retries")})
	if _, err := down.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 1, 0); !errors.Is(err, ErrIntegrationUnreachable) {
		t.Fatalf("TMDB down: err = %v, want ErrIntegrationUnreachable", err)
	}
}

// Switching a server to the other type would fail every request its routes
// send it, so the switch is refused while a route uses the server, and the
// preview flags a server already switched.
func TestServerTypeSwitchKeepsRoutesWorking(t *testing.T) {
	store := routingStore(RoutingFacts{})
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{ID: 129, KeywordIDs: []int{210024}}})

	switched := store.integrations[2] // radarr-anime, which the movie rule "Anime" sends to
	switched.PluginConfig = map[string]any{"service_kind": "sonarr", "root_folder": "/tv"}
	_, err := svc.UpdateIntegration(context.Background(), routeAdmin, switched)
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.FieldErrors["plugin_config.service_kind"], "Anime") {
		t.Fatalf("switch: err = %v, want a service_kind field error naming the route", err)
	}

	store.integrations[2] = switched
	preview, err := svc.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 129, 7)
	if err != nil {
		t.Fatal(err)
	}
	if hd := preview.Tiers[0]; !strings.Contains(hd.Reason, "a sonarr server") {
		t.Fatalf("hd tier = %+v, want the type mismatch noted", hd)
	}
}

// The first Radarr (Sonarr) added becomes Everything else for movies
// (series); later ones change nothing. Deleting the last server of its kind
// takes that Everything else with it, unless rules still route the media type.
func TestFirstServerBecomesEverythingElseDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	for _, stmt := range []string{
		`CREATE TABLE request_routes (LIKE public.request_routes INCLUDING ALL)`,
		`CREATE TRIGGER route_revision BEFORE INSERT OR UPDATE ON request_routes FOR EACH ROW EXECUTE FUNCTION public.advance_request_editor_revision()`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	add := func(id, kind string) {
		t.Helper()
		config := map[string]any{}
		if kind != "" {
			config["service_kind"] = kind
		}
		if _, err := repo.SaveIntegrationWithDefaults(ctx, Integration{ID: id, Name: id, Enabled: true, CapabilityID: "arr", PluginConfig: config}, true); err != nil {
			t.Fatal(err)
		}
	}
	fallbacks := func() map[string]string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT media_type, coalesce(hd_integration_id, '') FROM request_routes WHERE is_fallback`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var mediaType, id string
			if err := rows.Scan(&mediaType, &id); err != nil {
				t.Fatal(err)
			}
			out[mediaType] = id
		}
		return out
	}

	add("other", "") // a plugin that serves no media type named by the fixture's supported types
	if _, err := pool.Exec(ctx, `UPDATE request_integrations SET supported_media_types = '{audiobook}' WHERE id = 'other'`); err != nil {
		t.Fatal(err)
	}
	add("radarr-a", kindRadarr)
	add("radarr-b", kindRadarr)
	add("sonarr", kindSonarr)
	if got := fallbacks(); got["movie"] != "radarr-a" || got["series"] != "sonarr" || len(got) != 2 {
		t.Fatalf("fallbacks = %v, want movies to radarr-a and series to sonarr", got)
	}

	// radarr-a is Everything else and another Radarr exists: refused.
	if err := repo.DeleteIntegration(ctx, "radarr-a"); err == nil {
		t.Fatal("deleted the Everything else server while another Radarr remains")
	}
	// The sole Sonarr goes with its Everything else.
	if err := repo.DeleteIntegration(ctx, "sonarr"); err != nil {
		t.Fatalf("delete the sole Sonarr: %v", err)
	}
	if got := fallbacks(); got["series"] != "" {
		t.Fatalf("fallbacks = %v, want series cleared", got)
	}
	// A rule keeps Everything else, and so the server, in place.
	if _, err := pool.Exec(ctx, `INSERT INTO request_routes (id, media_type, position, name, conditions, hd_integration_id)
		VALUES ('anime', 'movie', 0, 'Anime', '{"anime":true}', 'radarr-b')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteIntegration(ctx, "radarr-b"); err == nil {
		t.Fatal("deleted a server a rule sends to")
	}

	// A Seerr connection already serves series, and a 4K-flagged server is no
	// HD destination: neither first Sonarr becomes Everything else.
	if _, err := repo.SaveIntegrationWithDefaults(ctx, Integration{ID: "seerr", Name: "seerr", Enabled: true, CapabilityID: "seerr",
		SupportedMediaTypes: []string{"series"}, PluginConfig: map[string]any{}}, true); err != nil {
		t.Fatal(err)
	}
	add("sonarr-2", kindSonarr)
	if got := fallbacks(); got["series"] != "" {
		t.Fatalf("fallbacks = %v, want series left to the Seerr connection", got)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM request_integrations WHERE id IN ('seerr', 'sonarr-2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveIntegrationWithDefaults(ctx, Integration{ID: "sonarr-4k", Name: "sonarr-4k", Enabled: true, CapabilityID: "arr",
		PluginConfig: map[string]any{"service_kind": kindSonarr, "is_4k": true}}, true); err != nil {
		t.Fatal(err)
	}
	if got := fallbacks(); got["series"] != "" {
		t.Fatalf("fallbacks = %v, want a 4K server left alone", got)
	}
}

func TestSingleServerFallbackMigrationDatabase(t *testing.T) {
	matches, err := filepath.Glob("../../migrations/sql/*_request_routes_single_server_fallback.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("find migration: %v %v", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	up = up[strings.Index(up, "-- +goose Up"):strings.Index(up, "-- +goose Down")]
	up = strings.NewReplacer("-- +goose StatementBegin", "", "-- +goose StatementEnd", "").Replace(up)

	_, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `CREATE TABLE request_routes (LIKE public.request_routes INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		id, kind, key string
		enabled       bool
	}{
		{"radarr", kindRadarr, "k", true},
		{"radarr-off", kindRadarr, "k", false}, // not usable: disabled
		{"sonarr-a", kindSonarr, "k", true},
		{"sonarr-b", kindSonarr, "k", true},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO request_integrations (id, name, enabled, capability_id, installation_id, api_key_ref, plugin_config)
			VALUES ($1, $1, $2, 'arr', 1, $3, jsonb_build_object('service_kind', $4::text))`, seed.id, seed.enabled, seed.key, seed.kind); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	var mediaType, hd string
	if err := pool.QueryRow(ctx, `SELECT media_type, hd_integration_id FROM request_routes WHERE is_fallback`).Scan(&mediaType, &hd); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM request_routes`).Scan(&n)
	if mediaType != "movie" || hd != "radarr" || n != 1 {
		t.Fatalf("seeded %d routes, movie to %q; want only movies to the one usable Radarr (two Sonarrs pick nothing)", n, hd)
	}
}

// The preview flags every server problem a routed submission would fail on,
// so an unusable route never reads as working.
func TestPreviewRouteFlagsUnusableServers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		unusable func(*Integration)
		want     string
	}{
		{"not bound", func(in *Integration) { in.InstallationID = nil }, "not bound to a plugin installation"},
		{"no key", func(in *Integration) { in.APIKeyRef = "" }, "has no API key"},
		{"does not take movies", func(in *Integration) { in.SupportedMediaTypes = []string{"series"} }, "does not take movies"},
		{"gone", func(in *Integration) { in.ID = "deleted" }, "no longer exists"},
	} {
		store := routingStore(RoutingFacts{})
		tc.unusable(&store.integrations[0]) // radarr-hd, the fallback's HD server
		svc := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{ID: 129}})
		preview, err := svc.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 129, 7)
		if err != nil {
			t.Fatalf("%s: preview: %v", tc.name, err)
		}
		if hd := preview.Tiers[0]; !strings.Contains(hd.Reason, tc.want) || !strings.HasSuffix(hd.Reason, "would fail.") {
			t.Errorf("%s: HD tier = %+v, want a failure note containing %q", tc.name, hd, tc.want)
		}
		if uhd := preview.Tiers[1]; uhd.Reason != "" {
			t.Errorf("%s: 4K tier = %+v, want no failure note for the working 4K server", tc.name, uhd)
		}
	}
}

// A route cannot send a media type to a server that does not take it, and a
// server a route uses cannot stop taking the route's media type.
func TestRoutesRespectSupportedMediaTypes(t *testing.T) {
	store := routingStore(RoutingFacts{})
	seriesOnly := routerInst("generic-series")
	seriesOnly.SupportedMediaTypes = []string{"series"}
	store.integrations = append(store.integrations, seriesOnly)
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{})

	route := Route{MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "generic-series"}}
	if fields := fieldErrors(t, svc.validateRoute(context.Background(), &route)); !strings.Contains(fields["hd.integration_id"], "does not take movies") {
		t.Fatalf("field errors = %v, want hd.integration_id refused", fields)
	}

	narrowed := store.integrations[2] // radarr-anime, which the movie rule "Anime" sends to
	narrowed.SupportedMediaTypes = []string{"series"}
	_, err := svc.UpdateIntegration(context.Background(), routeAdmin, narrowed)
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.FieldErrors["supported_media_types"], "Anime") {
		t.Fatalf("narrow: err = %v, want a supported_media_types field error naming the route", err)
	}
}

// HD versions go only to servers not marked 4K, and 4K versions only to
// servers marked 4K; a server can't flip its 4K switch while routes rely on
// the other.
func TestRoutesKeepHDAnd4KServersApart(t *testing.T) {
	store := routingStore(RoutingFacts{})
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{})
	ctx := context.Background()

	route := Route{MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)},
		HD:         RouteDestination{IntegrationID: "radarr-4k"}, UHD: RouteDestination{IntegrationID: "radarr-anime"}}
	fields := fieldErrors(t, svc.validateRoute(ctx, &route))
	if !strings.Contains(fields["hd.integration_id"], "marked 4K") {
		t.Fatalf("hd error = %q, want the 4K server refused for HD", fields["hd.integration_id"])
	}
	if !strings.Contains(fields["uhd.integration_id"], "isn't marked 4K") {
		t.Fatalf("uhd error = %q, want the HD server refused for 4K", fields["uhd.integration_id"])
	}

	route.HD.IntegrationID, route.UHD.IntegrationID = "radarr-anime", "radarr-4k"
	if err := svc.validateRoute(ctx, &route); err != nil {
		t.Fatalf("matching tiers: %v", err)
	}

	// Everything else sends 4K versions to radarr-4k; turning its switch off
	// would leave that 4K version on an HD server.
	unmarked := store.integrations[1]
	unmarked.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_4k": false}
	_, err := svc.UpdateIntegration(ctx, routeAdmin, unmarked)
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.FieldErrors["plugin_config.is_4k"], "Everything else") {
		t.Fatalf("unmark: err = %v, want an is_4k field error naming the route", err)
	}

	// The Anime rule sends HD versions to radarr-anime, so it can't be marked 4K.
	marked := store.integrations[2]
	marked.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_4k": true}
	_, err = svc.UpdateIntegration(ctx, routeAdmin, marked)
	if !errors.As(err, &verr) || !strings.Contains(verr.FieldErrors["plugin_config.is_4k"], "can't be marked 4K") {
		t.Fatalf("mark: err = %v, want an is_4k field error", err)
	}
}

// A server of another plugin (Seerr) has no 4K switch of ours and takes both
// versions, so routing can send it either.
func TestRoutesSendEitherVersionToSelfRoutedServers(t *testing.T) {
	store := routingStore(RoutingFacts{})
	seerr := routerInst("seerr")
	seerr.CapabilityID, seerr.PluginConfig = "seerr", map[string]any{}
	seerr.SupportedMediaTypes = []string{"movie"}
	store.integrations = append(store.integrations, seerr)
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{})

	route := Route{MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)},
		HD:         RouteDestination{IntegrationID: "seerr"}, UHD: RouteDestination{IntegrationID: "seerr"}}
	if err := svc.validateRoute(context.Background(), &route); err != nil {
		t.Fatalf("Seerr for both versions: %v", err)
	}
}

// Standard pauses the stored routes and the editor hides them, so a server's
// 4K switch can change there even when a paused route sends it the other
// version.
func TestStandardRoutingLeavesThe4KSwitchFree(t *testing.T) {
	store := &modeStore{fakeStore: routingStore(RoutingFacts{}), mode: RoutingStandard}
	svc := modeService(store, &fakeTMDBClient{})
	ctx := context.Background()

	// The paused Anime rule sends HD versions to radarr-anime.
	marked := store.integrations[2]
	marked.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_4k": true}
	if _, err := svc.UpdateIntegration(ctx, routeAdmin, marked); err != nil {
		t.Fatalf("mark under Standard: %v", err)
	}

	store.mode = RoutingAdvanced
	unmarked := store.integrations[1]
	unmarked.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_4k": false}
	_, err := svc.UpdateIntegration(ctx, routeAdmin, unmarked)
	var verr *ValidationError
	if !errors.As(err, &verr) || verr.FieldErrors["plugin_config.is_4k"] == "" {
		t.Fatalf("unmark under Advanced: err = %v, want an is_4k field error", err)
	}
}

// validateRoute reads the servers before the save; the save checks the tier
// again with the servers locked, so a 4K switch changed in between is caught.
func TestSaveRouteRechecksTierDatabase(t *testing.T) {
	ctx := t.Context()
	repo, pool := routingModeRepository(t)
	for _, in := range []Integration{arrServer("radarr", kindRadarr, nil), arrServer("radarr-4k", kindRadarr, map[string]any{"is_4k": true})} {
		in.APIKeyRef = ""
		if _, err := repo.SaveIntegrationWithDefaults(ctx, in, true); err != nil {
			t.Fatal(err)
		}
	}
	// radarr was marked 4K after the editor validated the route.
	if _, err := pool.Exec(ctx, `UPDATE request_integrations SET plugin_config = plugin_config || '{"is_4k": true}' WHERE id = 'radarr'`); err != nil {
		t.Fatal(err)
	}
	route := Route{ID: "anime", MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)},
		HD:         RouteDestination{IntegrationID: "radarr"}, UHD: RouteDestination{IntegrationID: "radarr-4k"}}
	_, err := repo.SaveRouteConditional(ctx, route, 0)
	fields := fieldErrors(t, err)
	if !strings.Contains(fields["hd.integration_id"], "marked 4K") || fields["uhd.integration_id"] != "" {
		t.Fatalf("fields = %v, want only the HD server refused", fields)
	}
	route.HD.IntegrationID = ""
	if _, err := repo.SaveRouteConditional(ctx, route, 0); err != nil {
		t.Fatalf("4K only: %v", err)
	}
}

// A server's type changed after the editor validated the route is caught by
// the same locked check: movies can't go to what is now a Sonarr.
func TestSaveRouteRechecksServerKindDatabase(t *testing.T) {
	ctx := t.Context()
	repo, pool := routingModeRepository(t)
	in := arrServer("radarr", kindRadarr, nil)
	in.APIKeyRef = ""
	if _, err := repo.SaveIntegrationWithDefaults(ctx, in, true); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE request_integrations
		SET plugin_config = plugin_config || '{"service_kind": "sonarr"}', supported_media_types = '{series}'
		WHERE id = 'radarr'`); err != nil {
		t.Fatal(err)
	}
	route := Route{ID: "anime", MediaType: MediaTypeMovie, Name: "Anime", Enabled: true,
		Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "radarr"}, SkipUHD: true}
	_, err := repo.SaveRouteConditional(ctx, route, 0)
	if msg := fieldErrors(t, err)["hd.integration_id"]; !strings.Contains(msg, "is a Sonarr server") {
		t.Fatalf("hd.integration_id = %q, want the server type refused", msg)
	}
}

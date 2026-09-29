package requests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// modeStore is the fake store with a routing mode.
type modeStore struct {
	*fakeStore
	mode RoutingMode
}

func (s *modeStore) GetRoutingSettings(context.Context) (RoutingSettings, error) {
	return RoutingSettings{Mode: s.mode, Revision: 1}, nil
}

func (s *modeStore) UpdateRoutingModeConditional(_ context.Context, mode RoutingMode, _ int64) (RoutingSettings, error) {
	s.mode = mode
	return RoutingSettings{Mode: mode, Revision: 2}, nil
}

func modeService(store *modeStore, tmdbClient *fakeTMDBClient) *Service {
	service := NewService(store, tmdbClient, &fakePresence{})
	service.Now = func() time.Time { return time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC) }
	service.SetUserRepository(requestUserRepo{})
	return service
}

func arrServer(id, kind string, config map[string]any) Integration {
	in := routerInst(id)
	in.Name = id
	in.PluginConfig = map[string]any{"service_kind": kind, "root_folder": "/" + id}
	for k, v := range config {
		in.PluginConfig[k] = v
	}
	return in
}

func TestStandardLayout(t *testing.T) {
	radarr := arrServer("radarr", kindRadarr, nil)
	radarr4K := arrServer("radarr-4k", kindRadarr, map[string]any{"is_4k": true})
	sonarr := arrServer("sonarr", kindSonarr, nil)

	layout, blocker := standardLayout([]Integration{radarr, radarr4K, sonarr})
	if blocker != "" || len(layout) != 2 ||
		layout[0] != (StandardDestination{MediaType: MediaTypeMovie, HDIntegrationID: "radarr", UHDIntegrationID: "radarr-4k"}) ||
		layout[1] != (StandardDestination{MediaType: MediaTypeSeries, HDIntegrationID: "sonarr"}) {
		t.Fatalf("layout = %+v blocker = %q, want Radarr + Radarr 4K for movies and Sonarr for series", layout, blocker)
	}

	second := arrServer("radarr-anime", kindRadarr, nil)
	if layout, blocker := standardLayout([]Integration{radarr, second, sonarr}); layout != nil ||
		!strings.Contains(blocker, "Movies can go to more than one server (radarr, radarr-anime)") {
		t.Fatalf("two Radarrs: layout = %+v blocker = %q", layout, blocker)
	}
	otherUHD := arrServer("radarr-4k-b", kindRadarr, map[string]any{"is_default_4k": true})
	if _, blocker := standardLayout([]Integration{radarr, radarr4K, otherUHD}); !strings.Contains(blocker, "More than one 4K server takes movies") {
		t.Fatalf("two 4K Radarrs: blocker = %q", blocker)
	}
	// A disabled server does not take requests, so it does not count.
	second.Enabled = false
	if _, blocker := standardLayout([]Integration{radarr, second}); blocker != "" {
		t.Fatalf("disabled second Radarr: blocker = %q", blocker)
	}
	// Another plugin that takes movies is a second movie server too.
	seerr := routerInst("seerr")
	seerr.Name, seerr.SupportedMediaTypes = "seerr", []string{"movie"}
	if _, blocker := standardLayout([]Integration{radarr, seerr, sonarr}); !strings.Contains(blocker, "Movies can go to more than one server") {
		t.Fatalf("Radarr + Seerr: blocker = %q", blocker)
	}
	// Seerr for movies with a Radarr marked 4K: with no rule Seerr would be
	// handed every tier, so Standard cannot use both.
	if layout, blocker := standardLayout([]Integration{seerr, radarr4K}); layout != nil ||
		!strings.Contains(blocker, "Movies go to seerr and their 4K versions to radarr-4k, which are different request services") ||
		!strings.Contains(blocker, "Switch to Advanced routing") {
		t.Fatalf("Seerr + 4K Radarr: layout = %+v blocker = %q", layout, blocker)
	}
	seerr4K := routerInstOn("seerr-4k", 2)
	seerr4K.Name, seerr4K.SupportedMediaTypes = "seerr-4k", []string{"movie"}
	seerr4K.PluginConfig = map[string]any{"is_default_4k": true}
	if _, blocker := standardLayout([]Integration{seerr, seerr4K}); !strings.Contains(blocker, "different request services") {
		t.Fatalf("two Seerr installations: blocker = %q", blocker)
	}
	// The same Seerr's 4K connection is one service, and Radarrs from
	// different plugins are routed tier by tier.
	sameSeerr4K := seerr4K
	sameSeerr4K.InstallationID, sameSeerr4K.CapabilityID = seerr.InstallationID, seerr.CapabilityID
	if _, blocker := standardLayout([]Integration{seerr, sameSeerr4K}); blocker != "" {
		t.Fatalf("one Seerr with a 4K connection: blocker = %q", blocker)
	}
	otherRadarr4K := radarr4K
	otherInstall := 2
	otherRadarr4K.InstallationID = &otherInstall
	if _, blocker := standardLayout([]Integration{radarr, otherRadarr4K}); blocker != "" {
		t.Fatalf("Radarrs from two plugins: blocker = %q", blocker)
	}
	// A media type served by another plugin alone keeps that plugin's routing.
	layout, _ = standardLayout([]Integration{seerr})
	if got := standardRoutes([]Integration{seerr}, layout, MediaTypeMovie); got != nil {
		t.Fatalf("Seerr-only movies: routes = %+v, want none so the plugin routes", got)
	}
}

// standardStore is routingStore's request with Standard on: one Radarr, one
// Radarr marked 4K, and a paused anime rule.
func standardStore(facts RoutingFacts) *modeStore {
	store := routingStore(facts)
	store.integrations = []Integration{
		arrServer("radarr-hd", kindRadarr, map[string]any{"is_default": false}),
		arrServer("radarr-4k", kindRadarr, map[string]any{"is_4k": true}),
	}
	store.routes = []Route{
		{ID: "r-anime", MediaType: MediaTypeMovie, Name: "Anime", Enabled: true, Conditions: RouteConditions{Anime: new(true)},
			HD: RouteDestination{IntegrationID: "radarr-4k", Overrides: map[string]any{"root_folder": "/anime"}}},
		{ID: "fallback-movie", MediaType: MediaTypeMovie, Position: 1000, Name: "Everything else", Enabled: true, IsFallback: true,
			HD: RouteDestination{IntegrationID: "radarr-hd", Overrides: map[string]any{"root_folder": "/elsewhere"}}},
	}
	return &modeStore{fakeStore: store, mode: RoutingStandard}
}

func TestStandardSendsEachTierToItsServerAsIs(t *testing.T) {
	// Facts were never captured and there is no TMDB client: Standard does
	// not need them.
	store := standardStore(RoutingFacts{})
	router := &fakeRouterProvider{}
	svc := modeService(store, &fakeTMDBClient{})
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})

	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(router.fulfillLog) != 2 {
		t.Fatalf("fulfill calls = %+v, want one per tier", router.fulfillLog)
	}
	hd, uhd := router.fulfillLog[0].conns[0], router.fulfillLog[1].conns[0]
	if hd.ID != "radarr-hd" || hd.Config["root_folder"] != "/radarr-hd" || hd.Config["is_default"] != true {
		t.Fatalf("HD = %+v, want the Radarr with its own root folder, not the paused rules' or Everything else's", hd)
	}
	if uhd.ID != "radarr-4k" || uhd.Config["root_folder"] != "/radarr-4k" || uhd.Config["is_default_4k"] != true {
		t.Fatalf("4K = %+v, want the Radarr marked 4K", uhd)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	for _, target := range targets {
		if target.RouteName != standardRouteName {
			t.Fatalf("target = %+v, want it stamped Standard", target)
		}
	}
	if _, fetched := store.factsSet["r1"]; fetched {
		t.Fatal("Standard fetched routing facts it does not use")
	}
}

func TestStandardWithoutA4KServerMakesNoCopy(t *testing.T) {
	store := standardStore(capturedFacts(RoutingFacts{}))
	store.integrations = store.integrations[:1]
	store.settings.ForceDualQuality = true
	router := &fakeRouterProvider{}
	svc := modeService(store, &fakeTMDBClient{})
	svc.SetRouterProvider(router)

	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 || targets[0].Quality != Quality1080p {
		t.Fatalf("targets = %+v, want HD only", targets)
	}
}

// Standard saved while a media type has two servers of a kind (it is turned
// off around such a change, so this is a race) routes with the rules.
func TestStandardWithTwoServersRoutesWithTheRules(t *testing.T) {
	store := standardStore(capturedFacts(RoutingFacts{Anime: true}))
	store.integrations[1].PluginConfig["is_4k"] = false
	router := &fakeRouterProvider{}
	svc := modeService(store, &fakeTMDBClient{})
	svc.SetRouterProvider(router)

	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if hd := router.fulfillLog[0].conns[0]; hd.ID != "radarr-4k" || hd.Config["root_folder"] != "/anime" {
		t.Fatalf("HD = %+v, want the anime rule's server and folder", hd)
	}
}

// switchingStore turns Advanced on, with Everything else, right after the
// rules are read, as a switch committed between two reads would.
type switchingStore struct {
	*modeStore
}

func (s *switchingStore) ListRoutes(ctx context.Context) ([]Route, error) {
	routes, err := s.modeStore.ListRoutes(ctx)
	s.mode = RoutingAdvanced
	s.routes = append(s.routes, Route{ID: FallbackRouteID(MediaTypeMovie), MediaType: MediaTypeMovie, Position: 1000,
		Name: fallbackRouteName, Enabled: true, IsFallback: true, HD: RouteDestination{IntegrationID: "radarr-hd"}})
	return routes, err
}

func TestFulfillContextReadsTheModeBeforeTheRules(t *testing.T) {
	store := standardStore(RoutingFacts{})
	store.routes = nil
	svc := modeService(store, &fakeTMDBClient{})
	svc.store = &switchingStore{modeStore: store}

	fc, err := svc.newFulfillContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Standard read before the switch routes with Standard; Advanced read
	// after it would have come with no rules at all.
	if !fc.standardOn && len(fc.routesFor(MediaTypeMovie)) == 0 {
		t.Fatalf("mode read after the rules: Advanced with the rules from before the switch (%+v)", fc.routes)
	}
}

func TestPreviewUnderStandard(t *testing.T) {
	store := standardStore(RoutingFacts{})
	store.integrations = store.integrations[:1]
	svc := modeService(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{ID: 129, Year: 2001}})

	preview, err := svc.PreviewRoute(context.Background(), Viewer{IsAdmin: true}, MediaTypeMovie, 129, 0)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	hd, uhd := preview.Tiers[0], preview.Tiers[1]
	if hd.IntegrationID != "radarr-hd" || hd.RouteName != standardRouteName || len(hd.Overrides) != 0 {
		t.Fatalf("HD tier = %+v, want Standard's Radarr", hd)
	}
	if uhd.Reason != "No server is marked 4K, so there is no 4K version." {
		t.Fatalf("4K tier = %+v", uhd)
	}
	if len(preview.Rules) != 1 || preview.Rules[0].Route.ID != standardRouteID(MediaTypeMovie) {
		t.Fatalf("rules = %+v, want only Standard's route", preview.Rules)
	}
}

// routingModeRepository is the lifecycle test schema with the route and mode
// tables, the mode set to Standard.
func routingModeRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	repo, pool := lifecycleTestRepository(t)
	for _, stmt := range []string{
		`CREATE TABLE request_routes (LIKE public.request_routes INCLUDING ALL)`,
		`CREATE TRIGGER route_revision BEFORE INSERT OR UPDATE ON request_routes FOR EACH ROW EXECUTE FUNCTION public.advance_request_editor_revision()`,
		`CREATE TRIGGER routing_revision BEFORE INSERT OR UPDATE ON request_routing FOR EACH ROW EXECUTE FUNCTION public.advance_request_editor_revision()`,
		`INSERT INTO request_routing (id, mode) VALUES (true, 'standard')`,
	} {
		if _, err := pool.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	return repo, pool
}

func TestRoutingModeDatabase(t *testing.T) {
	repo, pool := routingModeRepository(t)
	ctx := t.Context()
	svc := NewService(repo, &fakeTMDBClient{}, &fakePresence{})
	add := func(id, kind string, config map[string]any) {
		t.Helper()
		in := arrServer(id, kind, config)
		in.APIKeyRef = ""
		if _, err := repo.SaveIntegrationWithDefaults(ctx, in, true); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	mode := func() RoutingMode {
		t.Helper()
		o, err := svc.GetRoutingOverview(ctx, routeAdmin)
		if err != nil {
			t.Fatal(err)
		}
		return o.Mode
	}

	add("radarr", kindRadarr, nil)
	add("sonarr", kindSonarr, nil)
	add("radarr-4k", kindRadarr, map[string]any{"is_4k": true})
	o, err := svc.GetRoutingOverview(ctx, routeAdmin)
	if err != nil || o.Mode != RoutingStandard || o.StandardBlocker != "" || len(o.Standard) != 2 ||
		o.Standard[0].UHDIntegrationID != "radarr-4k" {
		t.Fatalf("overview = %+v %v, want Standard with Radarr 4K taking movies' 4K copies", o, err)
	}

	// A second normal Radarr turns Advanced on, and Everything else keeps
	// sending where Standard did: HD to radarr, 4K to radarr-4k.
	add("radarr-anime", kindRadarr, nil)
	if got := mode(); got != RoutingAdvanced {
		t.Fatalf("mode = %q after a second Radarr, want advanced", got)
	}
	var hd, uhd string
	if err := pool.QueryRow(ctx, `SELECT hd_integration_id, uhd_integration_id FROM request_routes WHERE id = 'fallback-movie'`).Scan(&hd, &uhd); err != nil {
		t.Fatal(err)
	}
	if hd != "radarr" || uhd != "radarr-4k" {
		t.Fatalf("Everything else = %q / %q, want radarr / radarr-4k", hd, uhd)
	}

	_, err = svc.UpdateRoutingModeConditional(ctx, routeAdmin, RoutingStandard, -1)
	if msg := fieldErrors(t, err)["mode"]; !strings.Contains(msg, "radarr, radarr-anime") {
		t.Fatalf("switch to Standard with two Radarrs: %q", msg)
	}

	// Turning the second Radarr off allows Standard again; it stays Advanced
	// until an admin switches.
	second, err := repo.GetIntegration(ctx, "radarr-anime")
	if err != nil {
		t.Fatal(err)
	}
	second.Enabled = false
	if _, err := repo.UpdateIntegrationConditional(ctx, *second, second.Revision); err != nil {
		t.Fatal(err)
	}
	if got := mode(); got != RoutingAdvanced {
		t.Fatalf("mode = %q, want Advanced kept", got)
	}
	if _, err := svc.UpdateRoutingModeConditional(ctx, routeAdmin, RoutingStandard, -1); err != nil {
		t.Fatalf("switch to Standard: %v", err)
	}
	// Turning it back on is adding a second Radarr again.
	second, _ = repo.GetIntegration(ctx, "radarr-anime")
	second.Enabled = true
	if _, err := repo.UpdateIntegrationConditional(ctx, *second, second.Revision); err != nil {
		t.Fatal(err)
	}
	if got := mode(); got != RoutingAdvanced {
		t.Fatalf("mode = %q after re-enabling the second Radarr, want advanced", got)
	}
	// A stale revision is refused.
	if _, err := svc.UpdateRoutingModeConditional(ctx, routeAdmin, RoutingAdvanced, 1); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale switch: %v", err)
	}
}

func TestRoutingModeMigrationDatabase(t *testing.T) {
	matches, err := filepath.Glob("../../migrations/sql/*_request_routing_mode.sql")
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

	for _, tc := range []struct {
		name  string
		setup []string
		want  RoutingMode
	}{
		{"one server of each kind", []string{`INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id) VALUES ('fallback-movie', 'movie', 1000, 'Everything else', true, 'radarr')`}, RoutingStandard},
		{"a 4K server", []string{`INSERT INTO request_integrations (id, name, enabled, capability_id, plugin_config) VALUES ('radarr-4k', 'radarr-4k', true, 'arr', '{"service_kind":"radarr","is_4k":true}')`}, RoutingStandard},
		{"a paused rule", []string{`INSERT INTO request_routes (id, media_type, position, name, enabled, conditions, hd_integration_id) VALUES ('r', 'movie', 0, 'Anime', false, '{"anime":true}', 'radarr')`}, RoutingStandard},
		{"a rule", []string{`INSERT INTO request_routes (id, media_type, position, name, conditions, hd_integration_id) VALUES ('r', 'movie', 0, 'Anime', '{"anime":true}', 'radarr')`}, RoutingAdvanced},
		{"a folder on Everything else", []string{`INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id, hd_overrides) VALUES ('fallback-movie', 'movie', 1000, 'Everything else', true, 'radarr', '{"root_folder":"/x"}')`}, RoutingAdvanced},
		{"4K copies from Everything else", []string{`INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id, uhd_integration_id) VALUES ('fallback-series', 'series', 1000, 'Everything else', true, 'sonarr', 'sonarr')`}, RoutingAdvanced},
		{"two Sonarrs", []string{`INSERT INTO request_integrations (id, name, enabled, capability_id, plugin_config) VALUES ('sonarr-2', 'sonarr-2', true, 'arr', '{"service_kind":"sonarr"}')`}, RoutingAdvanced},
		{"a Seerr taking movies", []string{`INSERT INTO request_integrations (id, name, enabled, capability_id, supported_media_types, plugin_config) VALUES ('seerr', 'seerr', true, 'seerr', '{movie}', '{}')`}, RoutingAdvanced},
		{"no 4K copies from Everything else while a server marked 4K exists", []string{
			`INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id) VALUES ('fallback-movie', 'movie', 1000, 'Everything else', true, 'radarr')`,
			`INSERT INTO request_integrations (id, name, enabled, capability_id, plugin_config) VALUES ('radarr-4k', 'radarr-4k', true, 'arr', '{"service_kind":"radarr","is_4k":true}')`,
		}, RoutingAdvanced},
		{"Everything else sending to Seerr", []string{
			`UPDATE request_integrations SET enabled = false WHERE id = 'radarr'`,
			`INSERT INTO request_integrations (id, name, enabled, capability_id, supported_media_types, plugin_config) VALUES ('seerr', 'seerr', true, 'seerr', '{movie}', '{}')`,
			`INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id) VALUES ('fallback-movie', 'movie', 1000, 'Everything else', true, 'seerr')`,
		}, RoutingAdvanced},
		{"a disabled second Sonarr", []string{`INSERT INTO request_integrations (id, name, enabled, capability_id, plugin_config) VALUES ('sonarr-2', 'sonarr-2', false, 'arr', '{"service_kind":"sonarr"}')`}, RoutingStandard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, pool := lifecycleTestRepository(t)
			ctx := t.Context()
			stmts := append([]string{
				`DROP TABLE request_routing`,
				`CREATE TABLE request_routes (LIKE public.request_routes INCLUDING ALL)`,
				`INSERT INTO request_integrations (id, name, enabled, capability_id, plugin_config) VALUES ('radarr', 'radarr', true, 'arr', '{"service_kind":"radarr"}'), ('sonarr', 'sonarr', true, 'arr', '{"service_kind":"sonarr"}')`,
			}, tc.setup...)
			for _, stmt := range append(stmts, up) {
				if _, err := pool.Exec(ctx, stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}
			var got RoutingMode
			if err := pool.QueryRow(ctx, `SELECT mode FROM request_routing`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAdvancedSeedUsesOnlyServersThatStillFitDatabase(t *testing.T) {
	ctx := t.Context()
	fallbacks := func(t *testing.T, pool *pgxpool.Pool) map[string][2]string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT media_type, coalesce(hd_integration_id, ''), coalesce(uhd_integration_id, '') FROM request_routes WHERE is_fallback`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][2]string{}
		for rows.Next() {
			var mediaType, hd, uhd string
			if err := rows.Scan(&mediaType, &hd, &uhd); err != nil {
				t.Fatal(err)
			}
			out[mediaType] = [2]string{hd, uhd}
		}
		return out
	}
	save := func(t *testing.T, repo *Repository, in Integration, create bool) {
		t.Helper()
		in.APIKeyRef = ""
		if _, err := repo.SaveIntegrationWithDefaults(ctx, in, create); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("Standard with Seerr for movies, add Radarr", func(t *testing.T) {
		repo, pool := routingModeRepository(t)
		seerr := routerInst("seerr")
		seerr.Name, seerr.CapabilityID, seerr.PluginConfig = "Seerr", "seerr", map[string]any{}
		seerr.SupportedMediaTypes = []string{"movie"}
		save(t, repo, seerr, true)
		radarr := arrServer("radarr", kindRadarr, nil)
		radarr.Name, radarr.SupportedMediaTypes = "Radarr", []string{"movie"}

		// With no rule for movies, the first server by name (Radarr) would
		// take the requests Seerr was getting: the save is refused.
		radarr.APIKeyRef = ""
		_, err := repo.SaveIntegrationWithDefaults(ctx, radarr, true)
		var verr *ValidationError
		if !errors.As(err, &verr) || !strings.Contains(verr.FormError, "Switch to Advanced routing and set Everything else for movies first") {
			t.Fatalf("add Radarr beside Seerr under Standard: %v, want a validation error", err)
		}
		if got, _ := repo.GetRoutingSettings(ctx); got.Mode != RoutingStandard {
			t.Fatalf("mode = %q, want Standard kept", got.Mode)
		}
		if _, err := repo.GetIntegration(ctx, "radarr"); err == nil {
			t.Fatal("Radarr was saved")
		}
		if got := fallbacks(t, pool); len(got) != 0 {
			t.Fatalf("Everything else = %v, want none", got)
		}

		// Following the message: Advanced, then Everything else to Seerr.
		// Radarr can be added, and movies keep going to Seerr.
		if _, err := repo.UpdateRoutingModeConditional(ctx, RoutingAdvanced, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id)
			VALUES ($1, 'movie', 1000, 'Everything else', true, 'seerr')`, FallbackRouteID(MediaTypeMovie)); err != nil {
			t.Fatal(err)
		}
		save(t, repo, radarr, true)
		if got := fallbacks(t, pool)["movie"]; got != [2]string{"seerr", ""} {
			t.Fatalf("Everything else for movies = %v, want Seerr", got)
		}
	})

	t.Run("Seerr keeps its media type when a save turns Advanced on for the other", func(t *testing.T) {
		repo, pool := routingModeRepository(t)
		seerr := routerInst("seerr")
		seerr.Name, seerr.CapabilityID, seerr.PluginConfig = "Seerr", "seerr", map[string]any{}
		seerr.SupportedMediaTypes = []string{"movie"}
		save(t, repo, seerr, true)
		for _, id := range []string{"sonarr-a", "sonarr-b"} {
			sonarr := arrServer(id, kindSonarr, nil)
			sonarr.SupportedMediaTypes = []string{"series"}
			save(t, repo, sonarr, true)
		}
		if got, _ := repo.GetRoutingSettings(ctx); got.Mode != RoutingAdvanced {
			t.Fatalf("mode = %q, want advanced with two Sonarrs", got.Mode)
		}
		got := fallbacks(t, pool)
		if _, ok := got["movie"]; ok {
			t.Fatalf("Everything else = %v, want movies left to Seerr", got)
		}
		if got["series"] != [2]string{"sonarr-a", ""} {
			t.Fatalf("Everything else for series = %v, want the Sonarr Standard used", got["series"])
		}
	})

	t.Run("a server switched to the other kind is not seeded for its old one", func(t *testing.T) {
		repo, pool := routingModeRepository(t)
		save(t, repo, arrServer("radarr", kindRadarr, nil), true)
		save(t, repo, arrServer("sonarr", kindSonarr, nil), true)
		// Standard from the migration: no Everything else yet.
		if _, err := pool.Exec(ctx, `DELETE FROM request_routes`); err != nil {
			t.Fatal(err)
		}
		switched, err := repo.GetIntegration(ctx, "radarr")
		if err != nil {
			t.Fatal(err)
		}
		switched.PluginConfig[configServiceKind] = kindSonarr
		save(t, repo, *switched, false)
		if got, _ := repo.GetRoutingSettings(ctx); got.Mode != RoutingAdvanced {
			t.Fatalf("mode = %q, want advanced with two Sonarrs", got.Mode)
		}
		got := fallbacks(t, pool)
		if _, ok := got["movie"]; ok {
			t.Fatalf("Everything else = %v, want no movie route to a server that is a Sonarr now", got)
		}
		if got["series"] != [2]string{"sonarr", ""} {
			t.Fatalf("Everything else for series = %v, want the Sonarr Standard used", got["series"])
		}
	})

	t.Run("a server marked 4K keeps the 4K server Standard used", func(t *testing.T) {
		for _, withFallback := range []bool{true, false} {
			repo, pool := routingModeRepository(t)
			save(t, repo, arrServer("radarr-a", kindRadarr, nil), true)
			save(t, repo, arrServer("radarr-4k", kindRadarr, map[string]any{"is_4k": true}), true)
			if !withFallback {
				// Standard from the migration: no Everything else yet.
				if _, err := pool.Exec(ctx, `DELETE FROM request_routes`); err != nil {
					t.Fatal(err)
				}
			}
			// radarr-a is marked 4K now, so Everything else no longer sends
			// it HD versions, whether it did before or not: no HD server is
			// left.
			wantHD := ""
			marked, err := repo.GetIntegration(ctx, "radarr-a")
			if err != nil {
				t.Fatal(err)
			}
			marked.PluginConfig["is_4k"] = true
			save(t, repo, *marked, false)
			if got, _ := repo.GetRoutingSettings(ctx); got.Mode != RoutingAdvanced {
				t.Fatalf("fallback=%v: mode = %q, want advanced with two 4K Radarrs", withFallback, got.Mode)
			}
			if got := fallbacks(t, pool)["movie"]; got != [2]string{wantHD, "radarr-4k"} {
				t.Fatalf("fallback=%v: Everything else = %v, want 4K still going to radarr-4k", withFallback, got)
			}
		}
	})

	t.Run("Standard with Seerr for movies, add a Radarr marked 4K", func(t *testing.T) {
		repo, pool := routingModeRepository(t)
		seerr := routerInst("seerr")
		seerr.Name, seerr.CapabilityID, seerr.PluginConfig = "Seerr", "seerr", map[string]any{}
		seerr.SupportedMediaTypes = []string{"movie"}
		save(t, repo, seerr, true)
		radarr4K := arrServer("radarr-4k", kindRadarr, map[string]any{"is_4k": true})
		radarr4K.Name, radarr4K.SupportedMediaTypes = "Radarr 4K", []string{"movie"}

		// Standard cannot send HD to Seerr and 4K to Radarr, so the save
		// turns Advanced on, and with no rule for movies Seerr's requests
		// would go to the first server by name: the save is refused.
		radarr4K.APIKeyRef = ""
		_, err := repo.SaveIntegrationWithDefaults(ctx, radarr4K, true)
		var verr *ValidationError
		if !errors.As(err, &verr) || !strings.Contains(verr.FormError, "Switch to Advanced routing and set Everything else for movies first") {
			t.Fatalf("add a 4K Radarr beside Seerr under Standard: %v, want a validation error", err)
		}
		if got, _ := repo.GetRoutingSettings(ctx); got.Mode != RoutingStandard {
			t.Fatalf("mode = %q, want Standard kept", got.Mode)
		}
		if _, err := repo.GetIntegration(ctx, "radarr-4k"); err == nil {
			t.Fatal("Radarr 4K was saved")
		}
		if got := fallbacks(t, pool); len(got) != 0 {
			t.Fatalf("Everything else = %v, want none", got)
		}
	})

	t.Run("Standard with Radarr for movies, add a Seerr marked 4K", func(t *testing.T) {
		repo, pool := routingModeRepository(t)
		svc := NewService(repo, &fakeTMDBClient{}, &fakePresence{})
		radarr := arrServer("radarr", kindRadarr, nil)
		radarr.Name, radarr.SupportedMediaTypes = "Radarr HD", []string{"movie"}
		save(t, repo, radarr, true)
		seerr := routerInstOn("seerr", 2)
		seerr.Name, seerr.CapabilityID, seerr.PluginConfig = "Seerr", "seerr", map[string]any{"is_default_4k": true}
		seerr.SupportedMediaTypes = []string{"movie"}
		save(t, repo, seerr, true)

		// Advanced is on, and HD keeps going to Radarr.
		o, err := svc.GetRoutingOverview(ctx, routeAdmin)
		if err != nil || o.Mode != RoutingAdvanced || !strings.Contains(o.StandardBlocker, "different request services") {
			t.Fatalf("overview = %+v %v, want Advanced with Standard unavailable", o, err)
		}
		if got := fallbacks(t, pool)["movie"]; got != [2]string{"radarr", ""} {
			t.Fatalf("Everything else for movies = %v, want Radarr", got)
		}
		_, err = svc.UpdateRoutingModeConditional(ctx, routeAdmin, RoutingStandard, -1)
		if msg := fieldErrors(t, err)["mode"]; !strings.Contains(msg, "Movies go to Radarr HD and their 4K versions to Seerr") {
			t.Fatalf("switch to Standard with Radarr and a 4K Seerr: %q", msg)
		}
	})

	t.Run("deleting a server under Standard ignores Everything else", func(t *testing.T) {
		repo, pool := routingModeRepository(t)
		save(t, repo, arrServer("radarr-a", kindRadarr, nil), true)
		save(t, repo, arrServer("radarr-4k", kindRadarr, map[string]any{"is_4k": true}), true)
		if got := fallbacks(t, pool)["movie"]; got[0] != "radarr-a" {
			t.Fatalf("Everything else = %v, want the first Radarr", got)
		}
		if err := repo.DeleteIntegration(ctx, "radarr-a"); err != nil {
			t.Fatalf("delete Standard's Radarr: %v", err)
		}
		// A paused rule still holds its server, and says how to change it.
		if _, err := pool.Exec(ctx, `INSERT INTO request_routes (id, media_type, position, name, conditions, uhd_integration_id)
			VALUES ('r', 'movie', 0, 'Anime', '{"anime":true}', 'radarr-4k')`); err != nil {
			t.Fatal(err)
		}
		err := repo.DeleteIntegration(ctx, "radarr-4k")
		var verr *ValidationError
		if !errors.As(err, &verr) || !strings.Contains(verr.FormError, "Switch to Advanced") {
			t.Fatalf("delete a server a paused rule uses: %v", err)
		}
	})
}

func TestStandardSendsAnimeSeriesWithTheAnimeSeriesType(t *testing.T) {
	for _, anime := range []bool{true, false} {
		store := standardStore(capturedFacts(RoutingFacts{Anime: anime}))
		store.integrations = []Integration{
			arrServer("sonarr", kindSonarr, map[string]any{"series_type": "standard"}),
			arrServer("sonarr-4k", kindSonarr, map[string]any{"is_4k": true, "series_type": "standard"}),
		}
		req := store.requests["r1"]
		req.MediaType, req.IsAnime = MediaTypeSeries, anime
		router := &fakeRouterProvider{}
		svc := modeService(store, &fakeTMDBClient{})
		svc.SetRouterProvider(router)
		svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})

		if _, err := svc.submitApprovedRequest(context.Background(), *req, Viewer{}, nil); err != nil {
			t.Fatalf("anime=%v: submit: %v", anime, err)
		}
		want := "standard"
		if anime {
			want = seriesTypeAnime
		}
		if len(router.fulfillLog) != 2 {
			t.Fatalf("anime=%v: fulfill calls = %+v, want HD and 4K", anime, router.fulfillLog)
		}
		for _, call := range router.fulfillLog {
			if conn := call.conns[0]; conn.Config[configSeriesType] != want {
				t.Errorf("anime=%v: %s to %s with series type %v, want %s", anime, call.qualities[0], conn.ID, conn.Config[configSeriesType], want)
			}
		}
		targets, _ := store.ListTargets(context.Background(), "r1")
		for _, target := range targets {
			if target.RouteName != standardRouteName {
				t.Errorf("anime=%v: target route %q, want Standard", anime, target.RouteName)
			}
		}
	}
}

func TestStandardRoutesLegacySeriesByItsAnimeFlagWhenTMDBIsDown(t *testing.T) {
	for _, anime := range []bool{true, false} {
		// A request from before routing facts were captured, with TMDB down:
		// Standard still routes it, by the anime flag stored with it.
		store := standardStore(RoutingFacts{})
		store.integrations = []Integration{arrServer("sonarr", kindSonarr, map[string]any{"series_type": "standard"})}
		req := store.requests["r1"]
		req.MediaType, req.IsAnime = MediaTypeSeries, anime
		router := &fakeRouterProvider{}
		svc := modeService(store, &fakeTMDBClient{detailErr: errors.New("tmdb unavailable")})
		svc.SetRouterProvider(router)

		if _, err := svc.submitApprovedRequest(context.Background(), *req, Viewer{}, nil); err != nil {
			t.Fatalf("anime=%v: submit: %v", anime, err)
		}
		want := "standard"
		if anime {
			want = seriesTypeAnime
		}
		if len(router.fulfillLog) != 1 {
			t.Fatalf("anime=%v: fulfill calls = %+v, want one", anime, router.fulfillLog)
		}
		if got := router.fulfillLog[0].conns[0].Config[configSeriesType]; got != want {
			t.Errorf("anime=%v: series type %v, want %s", anime, got, want)
		}
		if store.requests["r1"].RoutingFacts.Captured() {
			t.Errorf("anime=%v: facts stored as captured without TMDB", anime)
		}
	}
}

// A 4K switch changed under Standard can leave a paused route sending the
// other version to the server. Turning Advanced on, by hand or because the
// change broke Standard, clears those destinations: that version falls through
// to Everything else, which gets Standard's server.
func TestAdvancedClearsDestinationsThatChangedTierDatabase(t *testing.T) {
	ctx := t.Context()
	destinations := func(t *testing.T, pool *pgxpool.Pool, id string) [2]string {
		t.Helper()
		var hd, uhd string
		if err := pool.QueryRow(ctx, `SELECT coalesce(hd_integration_id, ''), coalesce(uhd_integration_id, '') FROM request_routes WHERE id = $1`, id).Scan(&hd, &uhd); err != nil {
			t.Fatal(err)
		}
		return [2]string{hd, uhd}
	}
	setup := func(t *testing.T, servers ...Integration) (*Service, *Repository, *pgxpool.Pool) {
		t.Helper()
		repo, pool := routingModeRepository(t)
		for _, in := range servers {
			in.APIKeyRef = ""
			if _, err := repo.SaveIntegrationWithDefaults(ctx, in, true); err != nil {
				t.Fatal(err)
			}
		}
		return NewService(repo, &fakeTMDBClient{}, &fakePresence{}), repo, pool
	}
	flip := func(t *testing.T, svc *Service, repo *Repository, id string, fourK bool) {
		t.Helper()
		in, err := repo.GetIntegration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		in.PluginConfig["is_4k"] = fourK
		if _, err := svc.UpdateIntegration(ctx, routeAdmin, *in); err != nil {
			t.Fatalf("flip %s under Standard: %v", id, err)
		}
	}

	t.Run("switched by hand", func(t *testing.T) {
		svc, repo, pool := setup(t, arrServer("radarr", kindRadarr, nil))
		if _, err := pool.Exec(ctx, `INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id, hd_overrides)
			VALUES ('fallback-movie', 'movie', 1000, 'Everything else', true, 'radarr', '{"root_folder":"/hd"}')
			ON CONFLICT (id) DO UPDATE SET hd_integration_id = 'radarr', hd_overrides = '{"root_folder":"/hd"}'`); err != nil {
			t.Fatal(err)
		}
		flip(t, svc, repo, "radarr", true)
		if _, err := svc.UpdateRoutingModeConditional(ctx, routeAdmin, RoutingAdvanced, -1); err != nil {
			t.Fatal(err)
		}
		if got := destinations(t, pool, "fallback-movie"); got != [2]string{"", "radarr"} {
			t.Fatalf("Everything else = %v, want no HD server and 4K to radarr", got)
		}
		var overrides string
		if err := pool.QueryRow(ctx, `SELECT hd_overrides::text FROM request_routes WHERE id = 'fallback-movie'`).Scan(&overrides); err != nil || overrides != "{}" {
			t.Fatalf("HD overrides = %q %v, want cleared with the server", overrides, err)
		}
	})

	t.Run("Advanced turned on by the change", func(t *testing.T) {
		svc, repo, pool := setup(t, arrServer("radarr", kindRadarr, nil), arrServer("radarr-4k", kindRadarr, map[string]any{"is_4k": true}))
		if _, err := pool.Exec(ctx, `INSERT INTO request_routes (id, media_type, position, name, enabled, conditions, hd_integration_id, uhd_integration_id)
			VALUES ('anime', 'movie', 0, 'Anime', true, '{"anime":true}', 'radarr', 'radarr-4k')`); err != nil {
			t.Fatal(err)
		}
		// Two normal Radarrs break Standard, which turns Advanced on.
		flip(t, svc, repo, "radarr-4k", false)
		if got, _ := repo.GetRoutingSettings(ctx); got.Mode != RoutingAdvanced {
			t.Fatalf("mode = %q, want Advanced", got.Mode)
		}
		if got := destinations(t, pool, "anime"); got != [2]string{"radarr", ""} {
			t.Fatalf("Anime = %v, want HD kept and 4K cleared", got)
		}
		if got := destinations(t, pool, FallbackRouteID(MediaTypeMovie)); got != [2]string{"radarr", ""} {
			t.Fatalf("Everything else = %v, want HD to radarr and no 4K server", got)
		}
	})
}

// The service checks a 4K switch change against the routes before the save,
// reading the mode then; the save checks again under the routing-mode lock, so
// Advanced turned on in between cannot leave a route sending a server the other
// version. A route saved before the rule does not block other edits.
func TestSaveRechecksTierUnderAdvancedDatabase(t *testing.T) {
	ctx := t.Context()
	repo, pool := routingModeRepository(t)
	for _, in := range []Integration{arrServer("radarr", kindRadarr, nil), arrServer("radarr-4k", kindRadarr, map[string]any{"is_4k": true})} {
		in.APIKeyRef = ""
		if _, err := repo.SaveIntegrationWithDefaults(ctx, in, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.UpdateRoutingModeConditional(ctx, RoutingAdvanced, -1); err != nil {
		t.Fatal(err)
	}
	// An older rule sending HD versions to the 4K server.
	if _, err := pool.Exec(ctx, `INSERT INTO request_routes (id, media_type, position, name, enabled, conditions, hd_integration_id)
		VALUES ('anime', 'movie', 0, 'Anime', true, '{"anime":true}', 'radarr-4k')`); err != nil {
		t.Fatal(err)
	}
	get := func(id string) Integration {
		t.Helper()
		in, err := repo.GetIntegration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return *in
	}

	flipped := get("radarr")
	flipped.PluginConfig["is_4k"] = true
	_, err := repo.UpdateIntegrationConditional(ctx, flipped, flipped.Revision)
	if msg := fieldErrors(t, err)["plugin_config.is_4k"]; !strings.Contains(msg, "Everything else") {
		t.Fatalf("conditional save marking radarr 4K: %q, want the is_4k error naming Everything else", msg)
	}
	if _, err := repo.SaveIntegrationWithDefaults(ctx, flipped, false); !strings.Contains(fieldErrors(t, err)["plugin_config.is_4k"], "can't be marked 4K") {
		t.Fatalf("save marking radarr 4K: %v, want an is_4k field error", err)
	}

	renamed := get("radarr-4k")
	renamed.Name = "Radarr UHD"
	if _, err := repo.UpdateIntegrationConditional(ctx, renamed, renamed.Revision); err != nil {
		t.Fatalf("rename the 4K server an older rule sends HD to: %v", err)
	}
}

// A route saved after the service checked the server's routes is found by the
// save's locked check, under Standard too: the server can't become a Sonarr
// while a rule sends it movies.
func TestSaveRechecksServerKindUnderLockDatabase(t *testing.T) {
	ctx := t.Context()
	repo, pool := routingModeRepository(t)
	in := arrServer("radarr", kindRadarr, nil)
	in.APIKeyRef = ""
	if _, err := repo.SaveIntegrationWithDefaults(ctx, in, true); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO request_routes (id, media_type, position, name, enabled, conditions, hd_integration_id)
		VALUES ('anime', 'movie', 0, 'Anime', true, '{"anime":true}', 'radarr')`); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetIntegration(ctx, "radarr")
	if err != nil {
		t.Fatal(err)
	}
	sonarr := *stored
	sonarr.PluginConfig["service_kind"] = kindSonarr
	sonarr.SupportedMediaTypes = []string{string(MediaTypeSeries)}
	_, err = repo.UpdateIntegrationConditional(ctx, sonarr, sonarr.Revision)
	if fields := fieldErrors(t, err); !strings.Contains(fields["plugin_config.service_kind"], "Anime") || !strings.Contains(fields["supported_media_types"], "Anime") {
		t.Fatalf("conditional save making radarr a Sonarr: %v, want the type and media type refused naming Anime", fields)
	}
	if _, err := repo.SaveIntegrationWithDefaults(ctx, sonarr, false); !strings.Contains(fieldErrors(t, err)["plugin_config.service_kind"], "Anime") {
		t.Fatalf("save making radarr a Sonarr: %v, want a service_kind field error", err)
	}
}

package requests

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

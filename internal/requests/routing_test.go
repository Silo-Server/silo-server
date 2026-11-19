package requests

import (
	"context"
	"errors"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

func capturedFacts(f RoutingFacts) RoutingFacts {
	at := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	f.CapturedAt = &at
	return f
}

func testRoutes() []Route {
	return []Route{
		{ID: "fallback", MediaType: MediaTypeMovie, Position: 1000, Name: "Everything else", Enabled: true, IsFallback: true,
			HD: RouteDestination{IntegrationID: "radarr-hd"}, UHD: RouteDestination{IntegrationID: "radarr-4k"}},
		{ID: "anime", MediaType: MediaTypeMovie, Position: 0, Name: "Anime", Enabled: true,
			Conditions: RouteConditions{Anime: boolPtr(true)},
			HD:         RouteDestination{IntegrationID: "radarr-anime", Overrides: map[string]any{"root_folder": "/anime"}}},
		{ID: "eighties", MediaType: MediaTypeMovie, Position: 1, Name: "80s", Enabled: false,
			Conditions: RouteConditions{YearFrom: 1980, YearTo: 1989},
			HD:         RouteDestination{IntegrationID: "radarr-retro"}},
	}
}

// routingStore seeds three Radarr servers and the routes above, plus an
// approved request for the given facts.
func routingStore(facts RoutingFacts) *fakeStore {
	store := newFakeStore()
	for _, id := range []string{"radarr-hd", "radarr-4k", "radarr-anime"} {
		in := routerInst(id)
		in.PluginConfig = map[string]any{"service_kind": "radarr", "root_folder": "/movies", "is_default": false, "anime_enabled": true}
		if id == "radarr-4k" {
			in.PluginConfig["is_4k"] = true
		}
		store.integrations = append(store.integrations, in)
	}
	store.routes = testRoutes()
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 129, Status: StatusApproved,
		Outcome: OutcomeActive, RequestedByUserID: 7, RoutingFacts: facts, IsAnime: facts.Anime}
	return store
}

func TestSubmitRoutedSendsEachTierToItsServer(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{Anime: true}))
	svc, router := newRoutedService(store)
	svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})

	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(router.fulfillLog) != 2 {
		t.Fatalf("fulfill calls = %+v, want one per tier", router.fulfillLog)
	}
	for _, call := range router.fulfillLog {
		if len(call.conns) != 1 || len(call.qualities) != 1 {
			t.Fatalf("call = %+v, want exactly one server and one quality", call)
		}
	}
	hd, uhd := router.fulfillLog[0], router.fulfillLog[1]
	hdConn := hd.conns[0]
	if hd.qualities[0] != Quality1080p || hdConn.ID != "radarr-anime" || hdConn.Config["root_folder"] != "/anime" ||
		hdConn.Config["is_default"] != true || hdConn.Config["is_default_4k"] != false || hdConn.Config["anime_enabled"] != false {
		t.Fatalf("HD call = %+v, want the Anime route's server as the HD default with its overrides and no plugin anime overlay", hd)
	}
	uhdConn := uhd.conns[0]
	if uhd.qualities[0] != Quality2160p || uhdConn.ID != "radarr-4k" || uhdConn.Config["is_default_4k"] != true ||
		uhdConn.Config["is_4k"] != true || uhdConn.Config["root_folder"] != "/movies" {
		t.Fatalf("4K call = %+v, want the fallback's 4K server as the 4K default", uhd)
	}
	// The server's stored config is untouched by the per-call overrides.
	if store.integrations[2].PluginConfig["root_folder"] != "/movies" {
		t.Fatal("routing overrides leaked into the stored server config")
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	names := map[Quality]string{}
	for _, target := range targets {
		names[target.Quality] = target.RouteName
	}
	if names[Quality1080p] != "Anime" || names[Quality2160p] != "Everything else" {
		t.Fatalf("target routes = %v, want each target stamped with its route", names)
	}
}

func TestReconcileKeepsStatusesWhenOnePluginFails(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInstOn("on-one", 1), routerInstOn("on-two", 2)}
	store.candidates = []*Request{{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 1, Status: StatusQueued, Outcome: OutcomeActive}}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "on-one", Quality: Quality1080p, Status: StatusQueued},
		{ID: 2, RequestID: "r1", IntegrationID: "on-two", Quality: Quality2160p, Status: StatusQueued},
	}}
	router := &fakeRouterProvider{
		statuses:     []RouterTargetStatus{{Quality: Quality1080p, ConnectionID: "on-one", Status: StatusDownloading}},
		statusErrFor: map[int]error{2: errors.New("sonarr 4K timeout")},
	}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	result, err := svc.ReconcileRequests(context.Background(), 10)
	if err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	if result.Errors != 1 {
		t.Fatalf("result = %+v, want the failing plugin counted", result)
	}
	if got := store.targets["r1"][0].Status; got != StatusDownloading {
		t.Fatalf("HD target = %s, want the working plugin's status applied", got)
	}
}

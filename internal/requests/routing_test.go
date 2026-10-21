package requests

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func boolPtr(v bool) *bool { return &v }

func capturedFacts(f RoutingFacts) RoutingFacts {
	at := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	f.CapturedAt = &at
	return f
}

func TestRouteConditionsMatch(t *testing.T) {
	spirited := Request{MediaType: MediaTypeMovie, RequestedByUserID: 7, RoutingFacts: capturedFacts(RoutingFacts{
		GenreIDs: []int{16, 14}, KeywordIDs: []int{210024}, OriginalLanguage: "ja", OriginCountries: []string{"JP"},
		Year: 2001, CompanyIDs: []int{10342}, Anime: true,
	})}
	for _, tc := range []struct {
		name string
		c    RouteConditions
		want bool
	}{
		{"no conditions", RouteConditions{}, true},
		{"anime", RouteConditions{Anime: boolPtr(true)}, true},
		{"not anime", RouteConditions{Anime: boolPtr(false)}, false},
		{"any genre", RouteConditions{GenreIDs: []int{28, 16}}, true},
		{"other genre", RouteConditions{GenreIDs: []int{28}}, false},
		{"keyword", RouteConditions{KeywordIDs: []int{210024}}, true},
		{"language, any case", RouteConditions{OriginalLanguages: []string{"JA", "ko"}}, true},
		{"other language", RouteConditions{OriginalLanguages: []string{"en"}}, false},
		{"country", RouteConditions{OriginCountries: []string{"jp"}}, true},
		{"the 2000s", RouteConditions{YearFrom: 2000, YearTo: 2009}, true},
		{"the 1980s", RouteConditions{YearFrom: 1980, YearTo: 1989}, false},
		{"from 2005 on", RouteConditions{YearFrom: 2005}, false},
		{"studio", RouteConditions{CompanyIDs: []int{10342}}, true},
		{"network", RouteConditions{NetworkIDs: []int{213}}, false},
		{"requester", RouteConditions{RequesterUserIDs: []int{7}}, true},
		{"other requester", RouteConditions{RequesterUserIDs: []int{8}}, false},
		{"every field must hold", RouteConditions{Anime: boolPtr(true), OriginalLanguages: []string{"en"}}, false},
	} {
		if got := tc.c.Matches(spirited); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
	undated := Request{RoutingFacts: capturedFacts(RoutingFacts{})}
	if (RouteConditions{YearTo: 1999}).Matches(undated) {
		t.Error("a year bound matched a title with no year")
	}
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

func TestDecideRoutesPerTier(t *testing.T) {
	anime := Request{MediaType: MediaTypeMovie, RoutingFacts: capturedFacts(RoutingFacts{Anime: true, Year: 1988})}
	got := decideRoutes(testRoutes(), anime, []Quality{Quality1080p, Quality2160p})
	if got[Quality1080p].RouteName != "Anime" || got[Quality1080p].IntegrationID != "radarr-anime" ||
		got[Quality1080p].Overrides["root_folder"] != "/anime" {
		t.Fatalf("HD = %+v, want the Anime route's server and overrides", got[Quality1080p])
	}
	// The Anime route has no 4K destination, so 4K falls through; the
	// disabled 80s route never matches.
	if got[Quality2160p].RouteName != "Everything else" || got[Quality2160p].IntegrationID != "radarr-4k" {
		t.Fatalf("4K = %+v, want the fallback", got[Quality2160p])
	}

	routes := testRoutes()
	routes[1].SkipUHD = true
	got = decideRoutes(routes, anime, []Quality{Quality1080p, Quality2160p})
	if d := got[Quality2160p]; !d.Skip || d.RouteName != "Anime" || d.IntegrationID != "" {
		t.Fatalf("4K = %+v, want skipped by the Anime route", d)
	}

	series := Request{MediaType: MediaTypeSeries, RoutingFacts: capturedFacts(RoutingFacts{})}
	if got := decideRoutes(testRoutes(), series, []Quality{Quality1080p}); len(got) != 0 {
		t.Fatalf("series decisions = %+v, want none from movie routes", got)
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

func TestSubmitRoutedDropsUnroutedFourK(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{}))
	store.routes[0].UHD = RouteDestination{}
	svc, router := newRoutedService(store)
	svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})

	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(router.fulfillLog) != 1 || router.fulfillLog[0].conns[0].ID != "radarr-hd" {
		t.Fatalf("fulfill calls = %+v, want only the HD tier to the fallback's server", router.fulfillLog)
	}
	if targets, _ := store.ListTargets(context.Background(), "r1"); len(targets) != 1 {
		t.Fatalf("targets = %+v, want only HD", targets)
	}
}

// Everything else with no HD server makes no HD copy when the title's 4K copy
// goes to a server, and leaves HD undecided when it does not.
func TestDecideRoutesSkipsHDWithoutAnHDServer(t *testing.T) {
	movie := Request{MediaType: MediaTypeMovie, RoutingFacts: capturedFacts(RoutingFacts{})}
	anime := Request{MediaType: MediaTypeMovie, RoutingFacts: capturedFacts(RoutingFacts{Anime: true})}
	both := []Quality{Quality1080p, Quality2160p}
	fourKOnly := func() []Route {
		routes := testRoutes()
		routes[0].HD = RouteDestination{}
		return routes
	}
	noServers := fourKOnly()
	noServers[0].UHD = RouteDestination{}
	animeTo4K := slices.Clone(noServers)
	animeTo4K[1].HD, animeTo4K[1].UHD = RouteDestination{}, RouteDestination{IntegrationID: "radarr-4k"}

	for _, tc := range []struct {
		name      string
		routes    []Route
		req       Request
		qualities []Quality
		// wantRoute is the route that decided HD, "" when nothing did.
		wantRoute string
		wantSkip  bool
		// wantStep is what Everything else did for HD.
		wantStep RouteStep
	}{
		{"4K goes out", fourKOnly(), movie, both, "fallback", true, RouteStepSkips},
		{"requester without 4K", fourKOnly(), movie, []Quality{Quality1080p}, "", false, RouteStepPasses},
		{"4K skipped too", noServers, movie, both, "", false, RouteStepPasses},
		{"a rule sends HD", fourKOnly(), anime, both, "anime", false, RouteStepDecided},
		{"a rule sends 4K", animeTo4K, anime, both, "fallback", true, RouteStepSkips},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decisions, traces := traceRoutes(tc.routes, tc.req, tc.qualities)
			hd, ok := decisions[Quality1080p]
			if ok != (tc.wantRoute != "") || hd.RouteID != tc.wantRoute || hd.Skip != tc.wantSkip {
				t.Fatalf("HD = %+v (decided %v), want route %q skip %v", hd, ok, tc.wantRoute, tc.wantSkip)
			}
			if got := traces[len(traces)-1].Steps[Quality1080p]; got != tc.wantStep {
				t.Fatalf("Everything else HD step = %q, want %q", got, tc.wantStep)
			}
		})
	}
}

// A requester without 4K has nothing to receive when no server takes HD, so
// the HD tier still fails and says why.
func TestSubmitRoutedFailsHDForARequesterWithout4K(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{}))
	store.routes[0].HD = RouteDestination{}
	svc, router := newRoutedService(store)
	svc.SetEntitlementResolver(fixedCeiling{q: "1080p"})

	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %+v, want none", router.fulfillLog)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 || targets[0].Quality != Quality1080p || targets[0].Status != StatusFailed ||
		targets[0].LastError != "no routing rule sends HD for this title" {
		t.Fatalf("targets = %+v, want a failed HD target naming the missing rule", targets)
	}
}

// A route whose server is disabled or not set up is an admin-fixable problem:
// with nothing sent yet, the submission retries instead of failing.
func TestSubmitRoutedRetriesWhenTheServerIsUnusable(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{}))
	store.integrations[0].Enabled = false // radarr-hd, the fallback's HD server
	svc, router := newRoutedService(store)

	req, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls != 0 || req.Status != StatusApproved || req.NextSubmitAt == nil ||
		!strings.Contains(req.LastError, `"radarr-hd", which is disabled`) {
		t.Fatalf("request = %+v (fulfill calls %d), want a retry scheduled with the disabled server named", req, router.fulfillCalls)
	}
}

// The reconcile service can be wired without a TMDB client; routing a request
// with uncaptured facts must then retry, not crash.
func TestReconcileRoutesWithoutTMDBClient(t *testing.T) {
	store := routingStore(RoutingFacts{})
	store.candidates = []*Request{store.requests["r1"]}
	svc := NewService(store, nil, &fakePresence{})
	svc.SetRouterProvider(&fakeRouterProvider{})

	result, err := svc.ReconcileRequests(context.Background(), 10)
	if err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	if result.Deferred != 1 || store.requests["r1"].Status != StatusApproved {
		t.Fatalf("result = %+v request = %+v, want the submission deferred", result, store.requests["r1"])
	}
}

func TestSubmitRoutedFetchesMissingFacts(t *testing.T) {
	store := routingStore(RoutingFacts{})
	tmdbClient := &fakeTMDBClient{detail: &tmdb.MediaDetail{ID: 129, KeywordIDs: []int{210024}, Year: 2001}}
	router := &fakeRouterProvider{}
	svc := newTestServiceWithTMDB(store, tmdbClient)
	svc.SetRouterProvider(router)

	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if facts := store.factsSet["r1"]; !facts.Captured() || !facts.Anime {
		t.Fatalf("stored facts = %+v, want the fetched anime facts", facts)
	}
	if len(router.fulfillLog) == 0 || router.fulfillLog[0].conns[0].ID != "radarr-anime" {
		t.Fatalf("fulfill calls = %+v, want the request routed on the fetched facts", router.fulfillLog)
	}
}

func TestSubmitRoutedWaitsWhenFactsCannotBeRead(t *testing.T) {
	store := routingStore(RoutingFacts{})
	router := &fakeRouterProvider{}
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{})
	svc.SetRouterProvider(router)

	req, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls != 0 || req.Status != StatusApproved || !strings.Contains(req.LastError, "TMDB") || req.NextSubmitAt == nil {
		t.Fatalf("request = %+v (fulfill calls %d), want nothing sent and a retry scheduled", req, router.fulfillCalls)
	}
}

func TestReconcileChecksEachTargetThroughItsOwnPlugin(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInstOn("on-one", 1), routerInstOn("on-two", 2)}
	store.candidates = []*Request{{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 1, Status: StatusQueued, Outcome: OutcomeActive}}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "on-one", Quality: Quality1080p, Status: StatusQueued, ExternalID: "a"},
		{ID: 2, RequestID: "r1", IntegrationID: "on-two", Quality: Quality2160p, Status: StatusDownloading, ExternalID: "b"},
	}}
	svc, router := newRoutedService(store)

	if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	installs := map[int][]string{}
	for _, call := range router.statusLog {
		for _, ref := range call.refs {
			installs[call.installationID] = append(installs[call.installationID], ref.ConnectionID)
		}
		if len(call.conns) != 1 {
			t.Fatalf("status call = %+v, want only its own server", call)
		}
	}
	if !slices.Equal(installs[1], []string{"on-one"}) || !slices.Equal(installs[2], []string{"on-two"}) {
		t.Fatalf("status checks by installation = %v, want each target through its own", installs)
	}
}

// A plugin may return a target without its connection. Such a target is
// still checked: through the plugin that routes the media type without rules,
// with all its connections, as before routing.
func TestReconcileChecksATargetWithoutAConnection(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInstOn("seerr-a", 3), routerInstOn("seerr-b", 3), routerInstOn("other", 4)}
	store.candidates = []*Request{{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 1, Status: StatusQueued, Outcome: OutcomeActive}}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", Quality: Quality1080p, Status: StatusQueued, ExternalID: "a"},
	}}
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{{Quality: Quality1080p, Status: StatusDownloading}}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	if len(router.statusLog) != 1 {
		t.Fatalf("status calls = %+v, want the unattributed target checked", router.statusLog)
	}
	call := router.statusLog[0]
	if call.installationID != 3 || len(call.conns) != 2 || len(call.refs) != 1 || call.refs[0].ExternalID != "a" {
		t.Fatalf("status call = %+v, want the first plugin with all its connections", call)
	}
	if got := store.targets["r1"][0].Status; got != StatusDownloading {
		t.Fatalf("target = %s, want the plugin's status applied", got)
	}
}

// Each routed call asks for one tier. A target the plugin labels with the
// other tier is dropped, so it cannot take that tier's slot from the target
// its own call returned, and a tier whose call returned only the other tier's
// target is recorded as failed.
func TestSubmitRoutedDropsTargetsForTheOtherTier(t *testing.T) {
	cases := []struct {
		name     string
		returned []RouterTarget
		want4K   func(*Target) bool
	}{
		{
			name: "each call returns both tiers",
			returned: []RouterTarget{
				{Quality: Quality1080p, ExternalID: "hd-target", Status: StatusQueued},
				{Quality: Quality2160p, ExternalID: "4k-target", Status: StatusQueued},
			},
			want4K: func(t *Target) bool { return t.Status == StatusQueued && t.IntegrationID == "radarr-4k" },
		},
		{
			name:     "the 4K call returns only an HD target",
			returned: []RouterTarget{{Quality: Quality1080p, ExternalID: "hd-target", Status: StatusQueued}},
			want4K: func(t *Target) bool {
				return t.Status == StatusFailed && t.IntegrationID == "radarr-4k" && t.LastError == msgNoTargetForQuality
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := routingStore(capturedFacts(RoutingFacts{Anime: true}))
			router := &fakeRouterProvider{targetsOverride: tc.returned}
			svc := newTestService(store)
			svc.SetRouterProvider(router)
			svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})

			if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil); err != nil {
				t.Fatalf("submit: %v", err)
			}
			targets, _ := store.ListTargets(context.Background(), "r1")
			byQuality := map[Quality]*Target{}
			for i := range targets {
				byQuality[targets[i].Quality] = &targets[i]
			}
			if hd := byQuality[Quality1080p]; hd == nil || hd.IntegrationID != "radarr-anime" || hd.Status != StatusQueued {
				t.Fatalf("targets = %+v, want the HD target on the Anime route's server", targets)
			}
			if uhd := byQuality[Quality2160p]; uhd == nil || !tc.want4K(uhd) {
				t.Fatalf("4K target = %+v, want it on the 4K route's server as returned by the 4K call", uhd)
			}
		})
	}
}

// A plugin that omits the connection from a routed target omits it from the
// target's status too; the target was recorded on its route's server, so the
// status still applies to it.
func TestReconcileAppliesAStatusWithoutAConnectionToTheRoutedServer(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInstOn("radarr-anime", 1)}
	store.candidates = []*Request{{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 1, Status: StatusQueued, Outcome: OutcomeActive}}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "radarr-anime", Quality: Quality1080p, Status: StatusQueued, ExternalID: "x"},
	}}
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{{Quality: Quality1080p, Status: StatusDownloading}}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	if got := store.targets["r1"][0].Status; got != StatusDownloading {
		t.Fatalf("target = %s, want the connectionless status applied to the routed server's target", got)
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

func TestSubmitRoutedRefusesAServerOfTheWrongKind(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{}))
	store.integrations[0].PluginConfig["service_kind"] = "sonarr" // radarr-hd, switched after the route was saved
	svc, router := newRoutedService(store)

	req, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls != 0 || req.NextSubmitAt == nil || !strings.Contains(req.LastError, "a sonarr server") {
		t.Fatalf("request = %+v (fulfill calls %d), want nothing sent and a retry with the mismatch named", req, router.fulfillCalls)
	}
}

func TestSubmitRoutedRefusesAServerThatDoesNotTakeTheMediaType(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{}))
	store.integrations[0].SupportedMediaTypes = []string{"series"} // radarr-hd, narrowed after the route was saved
	svc, router := newRoutedService(store)

	req, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls != 0 || req.NextSubmitAt == nil || !strings.Contains(req.LastError, "does not take them") {
		t.Fatalf("request = %+v (fulfill calls %d), want nothing sent and a retry with the mismatch named", req, router.fulfillCalls)
	}
}

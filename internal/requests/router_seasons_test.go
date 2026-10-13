package requests

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestRouterDescriptorCarriesSeasons(t *testing.T) {
	got := routerDescriptor(Request{MediaType: MediaTypeSeries, Seasons: []int{0, 2}}).GetSeasons()
	if !slices.Equal(got, []int32{0, 2}) {
		t.Fatalf("series seasons = %v, want [0 2]", got)
	}
	if got := routerDescriptor(Request{MediaType: MediaTypeSeries}).GetSeasons(); len(got) != 0 {
		t.Fatalf("whole-series seasons = %v, want none", got)
	}
	if got := routerDescriptor(Request{MediaType: MediaTypeMovie, Seasons: []int{1}}).GetSeasons(); len(got) != 0 {
		t.Fatalf("movie seasons = %v, want none", got)
	}
}

type featureResolver struct {
	fakeRouterResolver
	features RouterFeatures
}

func (r featureResolver) RouterFeatures(context.Context, int, string) (RouterFeatures, error) {
	return r.features, nil
}

// The plugin provider reads features through a resolver that can, and reports
// none, without launching a plugin, through one that cannot.
func TestPluginRouterProviderRouterFeatures(t *testing.T) {
	capable := NewPluginRouterProvider(featureResolver{features: RouterFeatures{SupportsSeasons: true}}).(RouterFeatureReader)
	if got, err := capable.RouterFeatures(context.Background(), 1, "arr"); err != nil || !got.SupportsSeasons {
		t.Fatalf("features = %+v, %v; want seasons", got, err)
	}
	plain := NewPluginRouterProvider(fakeRouterResolver{}).(RouterFeatureReader)
	if got, err := plain.RouterFeatures(context.Background(), 1, "arr"); err != nil || got.SupportsSeasons {
		t.Fatalf("features = %+v, %v; want none", got, err)
	}
}

// seriesRouterInst is a Sonarr connection bound to the given installation.
func seriesRouterInst(id string, installID int) Integration {
	in := routerInstOn(id, installID)
	in.SupportedMediaTypes = []string{"series"}
	in.PluginConfig = map[string]any{"service_kind": "sonarr", "is_default": true}
	return in
}

// With every series server on a plugin that takes seasons, a series in the
// library is requestable for its missing seasons.
func TestMissingSeasonsRequestableWithSeasonRouters(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{seriesRouterInst("sonarr", 1), seriesRouterInst("sonarr-4k", 1)}
	movies := routerInstOn("radarr", 2) // an old plugin that only takes movies
	movies.SupportedMediaTypes = []string{"movie"}
	store.integrations = append(store.integrations, movies)
	svc := seasonService(store, severanceInLibrary())
	svc.SetRouterProvider(&fakeRouterProvider{seasonCapable: map[int]bool{1: true}})

	status, err := svc.GetFeatureStatus(context.Background(), testViewer(1))
	if err != nil {
		t.Fatal(err)
	}
	if !status.MissingSeasonsRequestable {
		t.Fatal("status hides missing seasons with season-capable series servers")
	}
	detail, err := svc.GetDetail(context.Background(), testViewer(1), MediaTypeSeries, 95396)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Request.Requestable {
		t.Fatalf("request state = %+v, want requestable", detail.Request)
	}
	req, err := svc.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 95396, Title: "Severance"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !slices.Equal(req.Seasons, []int{2}) {
		t.Fatalf("seasons = %v, want the missing season 2", req.Seasons)
	}
}

// One series server on a plugin that cannot take seasons, or not bound to a
// plugin at all, keeps missing seasons off.
func TestMissingSeasonsNotRequestableWithAnOldSeriesRouter(t *testing.T) {
	unbound := seriesRouterInst("sonarr-unbound", 1)
	unbound.InstallationID = nil
	for name, second := range map[string]Integration{
		"old plugin": seriesRouterInst("sonarr-old", 2),
		"unbound":    unbound,
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			store.integrations = []Integration{seriesRouterInst("sonarr", 1), second}
			svc := seasonService(store, severanceInLibrary())
			svc.SetRouterProvider(&fakeRouterProvider{seasonCapable: map[int]bool{1: true}})
			status, err := svc.GetFeatureStatus(context.Background(), testViewer(1))
			if err != nil {
				t.Fatal(err)
			}
			if status.MissingSeasonsRequestable {
				t.Fatal("status offers missing seasons with a series server that would add the whole series")
			}
			if _, err := svc.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 95396, Title: "Severance"}); !errors.Is(err, ErrAlreadyAvailable) {
				t.Fatalf("err = %v, want ErrAlreadyAvailable", err)
			}
		})
	}
}

// A feature read that fails is an error, not a silent "no".
func TestMissingSeasonsRequestableReportsFeatureReadErrors(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{seriesRouterInst("sonarr", 1)}
	svc := seasonService(store, severanceInLibrary())
	svc.SetRouterProvider(&fakeRouterProvider{featuresErr: errors.New("db down")})
	if _, err := svc.GetFeatureStatus(context.Background(), testViewer(1)); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("err = %v, want the feature read error", err)
	}
}

func approvedSeasonRequest(store *fakeStore, id string, tmdbID int, seasons []int) {
	req := &Request{ID: id, MediaType: MediaTypeSeries, TMDBID: tmdbID, Title: "Severance", Status: StatusApproved,
		Outcome: OutcomeActive, RequestedByUserID: 7, Seasons: seasons}
	store.candidates = append(store.candidates, req)
	store.requests[id] = req
}

// A missing-seasons request goes to a season-capable plugin with its seasons,
// and waits for the library with any other.
func TestSubmitMissingSeasonsOnlyToSeasonRouters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		capable bool
	}{{"season plugin", true}, {"old plugin", false}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.integrations = []Integration{seriesRouterInst("sonarr", 1)}
			approvedSeasonRequest(store, "in-library", 95396, []int{2})
			router := &fakeRouterProvider{seasonCapable: map[int]bool{1: tc.capable}}
			svc := seasonService(store, severanceInLibrary())
			svc.SetRouterProvider(router)

			if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
				t.Fatal(err)
			}
			got := store.requests["in-library"]
			if !tc.capable {
				if router.fulfillCalls != 0 || got.Status != StatusApproved || got.SubmitAttempts != 0 {
					t.Fatalf("calls = %d, request = %+v; want approved and unsent, waiting for the library", router.fulfillCalls, got)
				}
				return
			}
			if router.fulfillCalls != 1 || !slices.Equal(router.gotSeasons[0], []int{2}) {
				t.Fatalf("calls = %d, seasons = %v; want one call for season 2", router.fulfillCalls, router.gotSeasons)
			}
			if got.Status == StatusApproved {
				t.Fatalf("request = %+v, want submitted", got)
			}
		})
	}
}

// A season request for a series not in the library goes to any plugin, as
// before; the descriptor still names its seasons.
func TestSubmitSeasonRequestOutsideTheLibraryToAnyRouter(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{seriesRouterInst("sonarr", 1)}
	approvedSeasonRequest(store, "absent", 1399, []int{1})
	router := &fakeRouterProvider{}
	svc := seasonService(store, severanceInLibrary())
	svc.SetRouterProvider(router)
	if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if router.fulfillCalls != 1 || !slices.Equal(router.gotSeasons[0], []int{1}) {
		t.Fatalf("calls = %d, seasons = %v; want one call naming season 1", router.fulfillCalls, router.gotSeasons)
	}
}

// seasonRoutes send anime series to a server on an old plugin and everything
// else to servers on a season plugin.
func seasonRoutes(store *fakeStore) {
	store.integrations = []Integration{
		seriesRouterInst("sonarr-hd", 1), seriesRouterInst("sonarr-4k", 1), seriesRouterInst("sonarr-anime", 2),
	}
	store.routes = []Route{
		{ID: "fallback", MediaType: MediaTypeSeries, Position: 1000, Name: "Everything else", Enabled: true, IsFallback: true,
			HD: RouteDestination{IntegrationID: "sonarr-hd"}, UHD: RouteDestination{IntegrationID: "sonarr-4k"}},
		{ID: "anime", MediaType: MediaTypeSeries, Position: 0, Name: "Anime", Enabled: true,
			Conditions: RouteConditions{Anime: boolPtr(true)}, HD: RouteDestination{IntegrationID: "sonarr-anime"}, SkipUHD: true},
	}
}

// With routing rules the servers chosen for the title decide: a mixed setup
// sends a missing-seasons request where the chosen servers take seasons, and
// holds it where one would add the whole series or the choice is unknown.
func TestSubmitRoutedMissingSeasonsToAMixedSetup(t *testing.T) {
	store := newFakeStore()
	seasonRoutes(store)
	approvedSeasonRequest(store, "drama", 95396, []int{2})
	store.requests["drama"].RoutingFacts = capturedFacts(RoutingFacts{})
	router := &fakeRouterProvider{seasonCapable: map[int]bool{1: true}}
	svc := seasonService(store, severanceInLibrary())
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})
	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["drama"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(router.fulfillLog) != 2 {
		t.Fatalf("fulfill calls = %+v, want HD and 4K on the season plugin", router.fulfillLog)
	}
	for i, call := range router.fulfillLog {
		if call.installationID != 1 || !slices.Equal(router.gotSeasons[i], []int{2}) {
			t.Fatalf("call %d = %+v with seasons %v, want installation 1 and season 2", i, call, router.gotSeasons[i])
		}
	}

	for name, tc := range map[string]struct {
		facts   RoutingFacts
		tmdbErr error
	}{
		"anime goes to the old plugin":        {facts: capturedFacts(RoutingFacts{Anime: true})},
		"facts not captured and TMDB is down": {tmdbErr: errors.New("tmdb down")},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			seasonRoutes(store)
			approvedSeasonRequest(store, "r", 95396, []int{2})
			store.requests["r"].RoutingFacts = tc.facts
			router := &fakeRouterProvider{seasonCapable: map[int]bool{1: true}}
			svc := seasonService(store, severanceInLibrary())
			svc.tmdb.(*fakeTMDBClient).detailErr = tc.tmdbErr
			svc.SetRouterProvider(router)
			got, err := svc.submitApprovedRequest(context.Background(), *store.requests["r"], Viewer{}, nil)
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if router.fulfillCalls != 0 || got.Status != StatusApproved || store.requests["r"].SubmitAttempts != 0 {
				t.Fatalf("calls = %d, request = %+v; want held for the library, unclaimed", router.fulfillCalls, got)
			}
		})
	}
}

// Should the routing facts read after the claim pick a server whose plugin
// cannot take seasons, the tier is not sent.
func TestSubmitRoutedMissingSeasonsRefusesAnOldPluginAfterTheClaim(t *testing.T) {
	store := newFakeStore()
	seasonRoutes(store)
	approvedSeasonRequest(store, "r", 95396, []int{2})
	store.requests["r"].RoutingFacts = capturedFacts(RoutingFacts{Anime: true})
	router := &fakeRouterProvider{seasonCapable: map[int]bool{1: true}}
	svc := seasonService(store, severanceInLibrary())
	svc.SetRouterProvider(router)
	fc, err := svc.newFulfillContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.submitClaimed(context.Background(), *store.requests["r"], Viewer{}, fc, true)
	if err == nil || !strings.Contains(err.Error(), "cannot fetch only the missing seasons") {
		t.Fatalf("err = %v, want the missing-seasons refusal", err)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want none", router.fulfillCalls)
	}
}

// Facts not captured yet are read before the servers are checked, so a
// request the rules send to season-capable servers is not held once TMDB
// answers.
func TestSubmitRoutedMissingSeasonsReadsTheFactsFirst(t *testing.T) {
	store := newFakeStore()
	seasonRoutes(store)
	approvedSeasonRequest(store, "r", 95396, []int{2})
	router := &fakeRouterProvider{seasonCapable: map[int]bool{1: true}}
	svc := seasonService(store, severanceInLibrary())
	svc.SetRouterProvider(router)
	if _, err := svc.submitApprovedRequest(context.Background(), *store.requests["r"], Viewer{}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls == 0 || !store.requests["r"].RoutingFacts.Captured() {
		t.Fatalf("calls = %d, facts = %+v; want the facts captured and the request sent", router.fulfillCalls, store.requests["r"].RoutingFacts)
	}
	for i, call := range router.fulfillLog {
		if call.installationID != 1 {
			t.Fatalf("call %d = %+v, want the season plugin", i, call)
		}
	}
}

// A season request approved after its seasons reached the library is not
// sent: the reconcile pass completes it from the library.
func TestSubmitSkipsSeasonsAlreadyInTheLibrary(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{seriesRouterInst("sonarr", 1)}
	approvedSeasonRequest(store, "done", 95396, []int{1})
	router := &fakeRouterProvider{seasonCapable: map[int]bool{1: true}}
	svc := seasonService(store, severanceInLibrary())
	svc.SetRouterProvider(router)
	got, err := svc.submitApprovedRequest(context.Background(), *store.requests["done"], Viewer{}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls != 0 || got.Status != StatusApproved || store.requests["done"].SubmitAttempts != 0 {
		t.Fatalf("calls = %d, request = %+v; want unsent, left for the library", router.fulfillCalls, got)
	}
	if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if router.fulfillCalls != 0 || store.requests["done"].Status != StatusCompleted {
		t.Fatalf("calls = %d, request = %+v; want completed from the library", router.fulfillCalls, store.requests["done"])
	}
}

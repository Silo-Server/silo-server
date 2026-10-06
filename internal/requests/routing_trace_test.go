package requests

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func ratingPtr(r string) *string { return &r }

func TestExcludeAndRatingConditions(t *testing.T) {
	facts := capturedFacts(RoutingFacts{OriginalLanguage: "ja", GenreIDs: []int{16}, ContentRating: ratingPtr("TV-14")})
	req := Request{MediaType: MediaTypeSeries, RequestedByUserID: 7, RoutingFacts: facts}
	for _, tc := range []struct {
		name string
		c    RouteConditions
		want []string
	}{
		{"not English matches a Japanese title", RouteConditions{ExcludeOriginalLanguages: []string{"en"}}, nil},
		{"not Japanese fails", RouteConditions{ExcludeOriginalLanguages: []string{"ja"}}, []string{condExcludeOriginalLanguages}},
		{"none of Animation fails", RouteConditions{ExcludeGenreIDs: []int{16, 99}}, []string{condExcludeGenreIDs}},
		{"not this requester fails", RouteConditions{ExcludeRequesterUserIDs: []int{7}}, []string{condExcludeRequesterUserIDs}},
		{"TV-14 is above PG", RouteConditions{MaxContentRating: "PG"}, []string{condMaxContentRating}},
		{"TV-14 is within R", RouteConditions{MaxContentRating: "R"}, nil},
		{"TV-14 is above TV-PG", RouteConditions{MaxContentRating: "TV-PG"}, []string{condMaxContentRating}},
		{"every failure is listed", RouteConditions{GenreIDs: []int{10751}, MaxContentRating: "PG"}, []string{condGenreIDs, condMaxContentRating}},
	} {
		if got := tc.c.Unmet(req); !slices.Equal(got, tc.want) {
			t.Errorf("%s: unmet = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A title with no US rating, or one never looked up, is not within any ceiling.
	for _, rating := range []*string{nil, ratingPtr("")} {
		unrated := Request{RoutingFacts: capturedFacts(RoutingFacts{ContentRating: rating})}
		if (RouteConditions{MaxContentRating: "R"}).Matches(unrated) {
			t.Errorf("rating %v matched a ceiling", rating)
		}
	}
}

func TestTraceExplainsEachRoute(t *testing.T) {
	routes := testRoutes()
	anime := Request{MediaType: MediaTypeMovie, RoutingFacts: capturedFacts(RoutingFacts{Anime: true, Year: 1988})}
	decisions, traces := traceRoutes(routes, anime, []Quality{Quality1080p, Quality2160p})
	if !maps2Equal(decisions, decideRoutes(routes, anime, []Quality{Quality1080p, Quality2160p})) {
		t.Fatal("trace and decideRoutes disagree")
	}
	got := map[string][2]RouteStep{}
	for _, tr := range traces {
		got[tr.Route.Name] = [2]RouteStep{tr.Steps[Quality1080p], tr.Steps[Quality2160p]}
	}
	want := map[string][2]RouteStep{
		"Anime":           {RouteStepSends, RouteStepPasses},
		"80s":             {RouteStepNoMatch, RouteStepNoMatch},
		"Everything else": {RouteStepDecided, RouteStepSends},
	}
	for name, steps := range want {
		if got[name] != steps {
			t.Errorf("%s: steps = %v, want %v", name, got[name], steps)
		}
	}
	if traces[len(traces)-1].Route.Name != "Everything else" {
		t.Fatal("Everything else is not last")
	}
}

func maps2Equal(a, b map[Quality]RouteDecision) bool {
	if len(a) != len(b) {
		return false
	}
	for q, d := range a {
		if b[q].RouteID != d.RouteID || b[q].IntegrationID != d.IntegrationID || b[q].Skip != d.Skip {
			return false
		}
	}
	return true
}

func TestRuleValidationForNewConditions(t *testing.T) {
	store := routingStore(RoutingFacts{})
	svc := newTestService(store)
	base := Route{MediaType: MediaTypeMovie, Name: "Kids", Enabled: true, HD: RouteDestination{IntegrationID: "radarr-hd"}}

	bad := base
	bad.Conditions = RouteConditions{MaxContentRating: "nonsense"}
	var verr *ValidationError
	if err := svc.validateRoute(context.Background(), &bad); !errors.As(err, &verr) || verr.FieldErrors["conditions.max_content_rating"] == "" {
		t.Fatalf("bad rating: err = %v", err)
	}
	both := base
	both.Conditions = RouteConditions{OriginalLanguages: []string{"ja"}, ExcludeOriginalLanguages: []string{"JA"}}
	if err := svc.validateRoute(context.Background(), &both); !errors.As(err, &verr) || verr.FieldErrors["conditions"] == "" {
		t.Fatalf("overlap: err = %v", err)
	}
	onlyExclude := base
	onlyExclude.Conditions = RouteConditions{ExcludeOriginalLanguages: []string{"en"}}
	if err := svc.validateRoute(context.Background(), &onlyExclude); err != nil {
		t.Fatalf("an exclude-only rule is a rule: %v", err)
	}
}

// A request captured before its US rating was is given one when a route
// checks ratings; a TMDB failure is a submission error, retried later.
func TestRatingIsCapturedLazily(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{}))
	store.routes = append(store.routes, Route{ID: "kids", MediaType: MediaTypeMovie, Name: "Kids", Enabled: true,
		Conditions: RouteConditions{MaxContentRating: "PG"}, HD: RouteDestination{IntegrationID: "radarr-anime"}})
	certs := &certTMDB{fakeTMDBClient: fakeTMDBClient{}, rating: "G"}
	svc := NewService(store, certs, &fakePresence{})
	svc.SetUserRepository(requestUserRepo{})
	req := *store.requests["r1"]
	if err := svc.ensureRoutingFacts(context.Background(), &req, store.routes); err != nil {
		t.Fatal(err)
	}
	if req.RoutingFacts.ContentRating == nil || *req.RoutingFacts.ContentRating != "G" || certs.calls != 1 {
		t.Fatalf("facts = %+v after %d calls, want rating G", req.RoutingFacts, certs.calls)
	}
	if err := svc.ensureRoutingFacts(context.Background(), &req, store.routes); err != nil || certs.calls != 1 {
		t.Fatalf("second pass looked the rating up again (%d calls, err %v)", certs.calls, err)
	}

	certs.err = errors.New("tmdb down")
	fresh := *store.requests["r1"]
	fresh.RoutingFacts.ContentRating = nil
	if err := svc.ensureRoutingFacts(context.Background(), &fresh, store.routes); err == nil {
		t.Fatal("a TMDB failure must fail the submission so it retries")
	}
}

type certTMDB struct {
	fakeTMDBClient
	rating string
	err    error
	calls  int
}

func (c *certTMDB) GetCertification(context.Context, string, int) (string, error) {
	c.calls++
	return c.rating, c.err
}

var _ TMDBCertificationClient = (*certTMDB)(nil)

// A ceiling compares ratings by their own ages, not parental-control tiers:
// "TV-Y7 or lower" does not take TV-PG.
func TestRatingCeilingUsesEachRatingsOwnAge(t *testing.T) {
	for _, tc := range []struct {
		rating, max string
		want        bool
	}{
		{"TV-PG", "TV-Y7", false},
		{"TV-Y", "TV-Y7", true},
		{"PG-13", "PG", false},
		{"G", "PG", true},
		{"NC-17", "R", false},
	} {
		if got := ratingWithin(ratingPtr(tc.rating), tc.max); got != tc.want {
			t.Errorf("%s within %s = %v, want %v", tc.rating, tc.max, got, tc.want)
		}
	}
}

// A title TMDB had not rated is asked about again after a day.
func TestUnratedTitleIsRecheckedLater(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{ContentRating: ratingPtr("")}))
	store.routes = append(store.routes, Route{ID: "kids", MediaType: MediaTypeMovie, Name: "Kids", Enabled: true,
		Conditions: RouteConditions{MaxContentRating: "PG"}, HD: RouteDestination{IntegrationID: "radarr-anime"}})
	certs := &certTMDB{rating: "PG"}
	svc := NewService(store, certs, &fakePresence{})
	req := *store.requests["r1"]
	captured := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	req.RoutingFacts.CapturedAt = &captured

	svc.Now = func() time.Time { return captured.Add(time.Hour) }
	if err := svc.ensureRoutingFacts(context.Background(), &req, store.routes); err != nil || certs.calls != 0 {
		t.Fatalf("asked again within a day (%d calls, err %v)", certs.calls, err)
	}
	svc.Now = func() time.Time { return captured.Add(25 * time.Hour) }
	if err := svc.ensureRoutingFacts(context.Background(), &req, store.routes); err != nil || certs.calls != 1 ||
		req.RoutingFacts.ContentRating == nil || *req.RoutingFacts.ContentRating != "PG" {
		t.Fatalf("after a day: %d calls, facts %+v, err %v; want the new rating", certs.calls, req.RoutingFacts, err)
	}
}

func TestHDCopyDropsTheServers4KFlag(t *testing.T) {
	install := 1
	fc := &fulfillContext{integrations: []Integration{{ID: "radarr-4k", Name: "Radarr 4K", Enabled: true, CapabilityID: "arr",
		InstallationID: &install, APIKeyRef: "k", PluginConfig: map[string]any{"service_kind": "radarr", "is_4k": true}}}}
	for q, want := range map[Quality]bool{Quality1080p: false, Quality2160p: true} {
		conn, _, _, err := routedConnection(fc, RouteDecision{RouteName: "Everything else", IntegrationID: "radarr-4k"}, MediaTypeMovie, q)
		if err != nil {
			t.Fatal(err)
		}
		if conn.Config[configIs4K] != want {
			t.Errorf("%s: is_4k = %v, want %v", q, conn.Config[configIs4K], want)
		}
	}
}

type certsTMDB struct {
	certTMDB
	all map[string][]string
}

func (c *certsTMDB) GetCertifications(context.Context, string, int) (map[string][]string, error) {
	return c.all, c.err
}

// A title never rated in the US is routed on its own country's rating.
func TestLazyRatingFallsBackToTheTitlesOwnCountry(t *testing.T) {
	store := routingStore(capturedFacts(RoutingFacts{OriginCountries: []string{"JP"}}))
	store.routes = append(store.routes, Route{ID: "kids", MediaType: MediaTypeMovie, Name: "Kids", Enabled: true,
		Conditions: RouteConditions{MaxContentRating: "PG-13"}, HD: RouteDestination{IntegrationID: "radarr-anime"}})
	certs := &certsTMDB{all: map[string][]string{"JP": {"G", "PG12"}, "FR": {"12"}}}
	svc := NewService(store, certs, &fakePresence{})
	req := *store.requests["r1"]
	if err := svc.ensureRoutingFacts(context.Background(), &req, store.routes); err != nil {
		t.Fatal(err)
	}
	if got := req.RoutingFacts.ContentRating; got == nil || *got != "JP:PG12" {
		t.Fatalf("rating = %v, want JP:PG12", got)
	}
	if !store.routes[len(store.routes)-1].Conditions.Matches(req) {
		t.Fatal("a Japanese PG12 title should match an at-most-PG-13 rule")
	}
	// With a US rating, the strictest US one is used, as GetCertification
	// picks it.
	certs.all["US"] = []string{"NR", "PG", "R"}
	req.RoutingFacts.ContentRating = nil
	if err := svc.ensureRoutingFacts(context.Background(), &req, store.routes); err != nil || *req.RoutingFacts.ContentRating != "R" {
		t.Fatalf("rating = %v, err %v; want the US R", req.RoutingFacts.ContentRating, err)
	}
}

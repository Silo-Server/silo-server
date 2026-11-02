package requests

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata/showstatus"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func ratingPtr(r string) *string { return &r }

func TestExcludeAndRatingConditions(t *testing.T) {
	facts := capturedFacts(RoutingFacts{OriginalLanguage: "ja", GenreIDs: []int{16}, ContentRating: ratingPtr("TV-14"), SeriesStatus: new("ended")})
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
		{"ended or canceled takes an ended show", RouteConditions{SeriesStatuses: []string{showstatus.Ended, showstatus.Canceled}}, nil},
		{"continuing or upcoming fails an ended show", RouteConditions{SeriesStatuses: []string{"returning", "in_production", "upcoming"}}, []string{condSeriesStatuses}},
		{"not ended fails an ended show", RouteConditions{ExcludeSeriesStatuses: []string{"ended"}}, []string{condExcludeSeriesStatuses}},
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
	// A series with no TMDB status has none: it fails a status list and
	// passes an exclude one.
	for _, status := range []*string{nil, new("")} {
		unknown := Request{MediaType: MediaTypeSeries, RoutingFacts: capturedFacts(RoutingFacts{SeriesStatus: status})}
		if (RouteConditions{SeriesStatuses: showstatus.Known}).Matches(unknown) ||
			!(RouteConditions{ExcludeSeriesStatuses: []string{"ended"}}).Matches(unknown) {
			t.Errorf("status %v: matched a status list or failed an exclude one", status)
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

	series := Route{MediaType: MediaTypeSeries, Name: "Ended", Enabled: true}
	for _, tc := range []struct {
		name       string
		mediaType  MediaType
		conditions RouteConditions
		field      string
	}{
		{"a movie rule on status", MediaTypeMovie, RouteConditions{ExcludeSeriesStatuses: []string{"ended"}}, "conditions.exclude_series_statuses"},
		{"TMDB's own spelling", MediaTypeSeries, RouteConditions{SeriesStatuses: []string{"Canceled"}}, "conditions.series_statuses"},
		{"a status both wanted and excluded", MediaTypeSeries, RouteConditions{SeriesStatuses: []string{"ended"}, ExcludeSeriesStatuses: []string{"ENDED"}}, "conditions"},
	} {
		route := series
		route.MediaType, route.Conditions = tc.mediaType, tc.conditions
		if err := svc.validateRoute(context.Background(), &route); !errors.As(err, &verr) || verr.FieldErrors[tc.field] == "" {
			t.Errorf("%s: err = %v, want an error on %s", tc.name, err, tc.field)
		}
	}
}

// A series captured before its status was is given one when a route checks
// status, read once: the capture time, which times the rating's recheck,
// stays, and a status already stored is not read again, so a show that ends
// after it was requested keeps its route. When another API server stored a
// status first, its value wins.
func TestSeriesStatusIsReadOnce(t *testing.T) {
	ctx := context.Background()
	store := routingStore(capturedFacts(RoutingFacts{}))
	store.requests["r1"].MediaType = MediaTypeSeries
	tmdbClient := &fakeTMDBClient{detail: &tmdb.MediaDetail{MediaType: "series", ID: 129, Status: "Returning Series"}}
	svc := newTestServiceWithTMDB(store, tmdbClient)
	routes := append(testRoutes(), Route{ID: "ended", MediaType: MediaTypeSeries, Name: "Ended", Enabled: true,
		Conditions: RouteConditions{SeriesStatuses: []string{showstatus.Ended, showstatus.Canceled}}, HD: RouteDestination{IntegrationID: "sonarr-ended"}})
	req := *store.requests["r1"]
	captured := *req.RoutingFacts.CapturedAt

	if err := svc.ensureRoutingFacts(ctx, &req, testRoutes()); err != nil || tmdbClient.detailCalls != 0 {
		t.Fatalf("no status route: %d detail reads, err %v; want none", tmdbClient.detailCalls, err)
	}
	if err := svc.ensureRoutingFacts(ctx, &req, routes); err != nil {
		t.Fatal(err)
	}
	if s := req.RoutingFacts.SeriesStatus; s == nil || *s != "returning" || !req.RoutingFacts.CapturedAt.Equal(captured) || tmdbClient.detailCalls != 1 {
		t.Fatalf("facts = %+v after %d reads, want status returning and the capture time kept", req.RoutingFacts, tmdbClient.detailCalls)
	}

	tmdbClient.detail = &tmdb.MediaDetail{MediaType: "series", ID: 129, Status: "Ended"}
	if err := svc.ensureRoutingFacts(ctx, &req, routes); err != nil || tmdbClient.detailCalls != 1 || *req.RoutingFacts.SeriesStatus != "returning" {
		t.Fatalf("second pass: facts %+v after %d reads, err %v; want the stored returning", req.RoutingFacts, tmdbClient.detailCalls, err)
	}
	if decision, ok := decideRoutes(routes, req, []Quality{Quality1080p})[Quality1080p]; ok {
		t.Fatalf("a show that ended after it was requested went to %q", decision.RouteName)
	}

	// This copy was read before another server stored "returning".
	stale := req
	stale.RoutingFacts.SeriesStatus = nil
	if err := svc.ensureRoutingFacts(ctx, &stale, routes); err != nil || stale.RoutingFacts.SeriesStatus == nil || *stale.RoutingFacts.SeriesStatus != "returning" {
		t.Fatalf("status %v, err %v; want the status stored first", stale.RoutingFacts.SeriesStatus, err)
	}

	store.requests["r1"].RoutingFacts.SeriesStatus = nil
	stale.RoutingFacts.SeriesStatus = nil
	tmdbClient.detail, tmdbClient.detailErr = nil, errors.New("tmdb down")
	if err := svc.ensureRoutingFacts(ctx, &stale, routes); err == nil {
		t.Fatal("a TMDB failure must fail the submission so it retries")
	}
	// A series TMDB no longer lists has no status, and is sent.
	tmdbClient.detailErr = tmdb.ErrNotFound
	if err := svc.ensureRoutingFacts(ctx, &stale, routes); err != nil || stale.RoutingFacts.SeriesStatus == nil || *stale.RoutingFacts.SeriesStatus != "" {
		t.Fatalf("status %v, err %v; want none, and no error", stale.RoutingFacts.SeriesStatus, err)
	}

	// A copy read before another server captured the facts, without a
	// status (a server not yet upgraded), still gets one.
	store.requests["r1"].RoutingFacts.SeriesStatus = nil
	uncaptured := stale
	uncaptured.RoutingFacts = RoutingFacts{}
	tmdbClient.detail, tmdbClient.detailErr = &tmdb.MediaDetail{MediaType: "series", ID: 129, Status: "Ended"}, nil
	if err := svc.ensureRoutingFacts(ctx, &uncaptured, routes); err != nil ||
		uncaptured.RoutingFacts.SeriesStatus == nil || *uncaptured.RoutingFacts.SeriesStatus != showstatus.Ended {
		t.Fatalf("facts %+v, err %v; want the stored capture with status ended", uncaptured.RoutingFacts, err)
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
	// Another API server stores a fact after this copy was read; writing
	// the rating keeps it.
	store.requests["r1"].RoutingFacts.SeriesStatus = new("ended")
	if err := svc.ensureRoutingFacts(context.Background(), &req, store.routes); err != nil {
		t.Fatal(err)
	}
	if req.RoutingFacts.ContentRating == nil || *req.RoutingFacts.ContentRating != "G" || certs.calls != 1 ||
		req.RoutingFacts.SeriesStatus == nil {
		t.Fatalf("facts = %+v after %d calls, want rating G and the stored status kept", req.RoutingFacts, certs.calls)
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
		conn, _, _, err := (&Service{}).routedConnection(context.Background(), fc, RouteDecision{RouteName: "Everything else", IntegrationID: "radarr-4k"}, MediaTypeMovie, q)
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

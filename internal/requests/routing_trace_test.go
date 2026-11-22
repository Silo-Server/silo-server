package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/showstatus"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func ratingPtr(r string) *string { return &r }

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

type certsTMDB struct {
	certTMDB
	all map[string][]string
}

func (c *certsTMDB) GetCertifications(context.Context, string, int) (map[string][]string, error) {
	return c.all, c.err
}

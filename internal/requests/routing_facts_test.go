package requests

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata/showstatus"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func TestCreateRequestCapturesRoutingFactsAndServerTitle(t *testing.T) {
	store := newFakeStore()
	tmdbClient := &fakeTMDBClient{detail: &tmdb.MediaDetail{
		MediaType: "movie", ID: 129, Title: "Spirited Away", Year: 2001,
		GenreIDs: []int{16, 14}, KeywordIDs: []int{210024}, OriginalLanguage: "ja",
		OriginCountries: []string{"JP"}, CompanyIDs: []int{10342},
	}}
	svc := newTestServiceWithTMDB(store, tmdbClient)

	req, err := svc.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie, TMDBID: 129, Title: "spirited away (client copy)",
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if req.Title != "Spirited Away" || store.created[0].Input.Year == nil || *store.created[0].Input.Year != 2001 {
		t.Fatalf("title/year = %q/%v, want the server's TMDB copy", req.Title, store.created[0].Input.Year)
	}
	facts := store.created[0].Facts
	if !facts.Captured() || !facts.Anime || !req.IsAnime || facts.OriginalLanguage != "ja" || facts.Year != 2001 ||
		!slices.Equal(facts.GenreIDs, []int{16, 14}) || !slices.Equal(facts.CompanyIDs, []int{10342}) || facts.SeriesStatus != nil {
		t.Fatalf("facts = %+v, want the TMDB snapshot", facts)
	}
}

// A series' status is captured with the request, in the catalog's terms.
func TestCreateSeriesRequestCapturesItsStatus(t *testing.T) {
	store := newFakeStore()
	detail := severanceDetail()
	detail.Status = "Canceled"
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: detail})

	if _, err := svc.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 95396, Title: "Severance"}); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if status := store.created[0].Facts.SeriesStatus; status == nil || *status != showstatus.Canceled {
		t.Fatalf("series status = %v, want %s", status, showstatus.Canceled)
	}
}

func TestCreateRequestWithoutTMDBDetailLeavesFactsUncaptured(t *testing.T) {
	store := newFakeStore()
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{})

	req, err := svc.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie, TMDBID: 550, Title: "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if req.Title != "Fight Club" || store.created[0].Facts.Captured() {
		t.Fatalf("title = %q facts = %+v, want the client's title and uncaptured facts", req.Title, store.created[0].Facts)
	}
}

// The capture keeps the first facts any API server stored. Facts read after
// it are merged into the stored ones key by key: a filled fact (series
// status) keeps the first value stored, and an updated one (the rating)
// replaces only itself, so neither write erases a fact another server stored
// meanwhile.
func TestRoutingFactsMergeDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "series", 1, 1399, StatusApproved)
	captured := capturedFacts(RoutingFacts{GenreIDs: []int{18}, Anime: true, ContentRating: new("")})
	if _, err := repo.SetRoutingFacts(ctx, "series", captured); err != nil {
		t.Fatal(err)
	}

	// A second capture, from a caller that read the facts before they were
	// stored, keeps the first.
	if again, err := repo.SetRoutingFacts(ctx, "series", capturedFacts(RoutingFacts{GenreIDs: []int{35}})); err != nil ||
		!again.IsAnime || !slices.Equal(again.RoutingFacts.GenreIDs, []int{18}) {
		t.Fatalf("second capture: facts = %+v, err %v; want the first capture kept", again.RoutingFacts, err)
	}

	first, err := repo.FillRoutingFacts(ctx, "series", RoutingFacts{SeriesStatus: new("returning")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.FillRoutingFacts(ctx, "series", RoutingFacts{SeriesStatus: new("ended")})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []*Request{first, second} {
		if s := req.RoutingFacts.SeriesStatus; s == nil || *s != "returning" || !req.IsAnime || !slices.Equal(req.RoutingFacts.GenreIDs, []int{18}) {
			t.Fatalf("after fill: facts = %+v, anime %v; want status returning and the captured facts kept", req.RoutingFacts, req.IsAnime)
		}
	}

	later := captured.CapturedAt.Add(48 * time.Hour)
	updated, err := repo.UpdateRoutingFacts(ctx, "series", RoutingFacts{ContentRating: new("TV-14"), CapturedAt: &later})
	if err != nil {
		t.Fatal(err)
	}
	f := updated.RoutingFacts
	if f.ContentRating == nil || *f.ContentRating != "TV-14" || !f.CapturedAt.Equal(later) ||
		f.SeriesStatus == nil || *f.SeriesStatus != "returning" || !f.Anime || !slices.Equal(f.GenreIDs, []int{18}) {
		t.Fatalf("after update: facts = %+v; want the new rating and capture time, the rest kept", f)
	}
}

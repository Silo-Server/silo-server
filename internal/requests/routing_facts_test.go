package requests

import (
	"slices"
	"testing"
	"time"
)

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

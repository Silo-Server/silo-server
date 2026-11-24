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
	if _, err := repo.SetRoutingFacts(ctx, "series", captured, nil); err != nil {
		t.Fatal(err)
	}

	// A second capture, from a caller that read the facts before they were
	// stored, keeps the first.
	if again, err := repo.SetRoutingFacts(ctx, "series", capturedFacts(RoutingFacts{GenreIDs: []int{35}}), nil); err != nil ||
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

// A season request's season details survive the store: the router gets
// them on every later submit and status check, not only the first.
func TestSeasonDetailsRoundTripDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	details := []SeasonDetail{{Number: 1, FirstAirDate: "2024-10-04", EpisodeCount: 24}, {Number: 2}}
	created, err := repo.CreateRequest(t.Context(), CreateRequestRecord{
		ID:            "dandadan",
		Input:         CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 240411, Title: "DAN DA DAN", Seasons: []int{1, 2}},
		Status:        StatusApproved,
		Outcome:       OutcomeActive,
		SeasonDetails: details,
		Requester:     Viewer{UserID: 1, ProfileID: "profile"},
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := repo.GetRequest(t.Context(), "dandadan")
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []*Request{created, read} {
		if !slices.Equal(req.SeasonDetails, details) {
			t.Fatalf("season details = %+v, want %+v", req.SeasonDetails, details)
		}
	}
	whole := insertLifecycleRequest(t, repo, "movie", 1, 550, StatusApproved)
	if len(whole.SeasonDetails) != 0 {
		t.Fatalf("request without details reads back %+v, want none", whole.SeasonDetails)
	}

	// Facts read at submission fill the details of a request made while TMDB
	// was unreachable, and leave stored ones alone.
	read2 := []SeasonDetail{{Number: 1, FirstAirDate: "2023-09-29", EpisodeCount: 28}}
	if _, err := repo.CreateRequest(t.Context(), CreateRequestRecord{
		ID:        "frieren",
		Input:     CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 209867, Title: "Frieren", Seasons: []int{1}},
		Status:    StatusApproved,
		Outcome:   OutcomeActive,
		Requester: Viewer{UserID: 1, ProfileID: "profile"},
	}); err != nil {
		t.Fatal(err)
	}
	filled, err := repo.SetRoutingFacts(t.Context(), "frieren", capturedFacts(RoutingFacts{}), read2)
	if err != nil || !slices.Equal(filled.SeasonDetails, read2) {
		t.Fatalf("details after the facts were read = %+v, err %v; want %+v", filled.SeasonDetails, err, read2)
	}
	kept, err := repo.SetRoutingFacts(t.Context(), "dandadan", capturedFacts(RoutingFacts{}), read2)
	if err != nil || !slices.Equal(kept.SeasonDetails, details) {
		t.Fatalf("stored details after the facts were read = %+v, err %v; want %+v kept", kept.SeasonDetails, err, details)
	}
}

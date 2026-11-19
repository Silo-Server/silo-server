package requests

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// severanceDetail is a series with two aired seasons, one announced, and
// specials. The test clock (newTestService) is 2026-05-24.
func severanceDetail() *tmdb.MediaDetail {
	return &tmdb.MediaDetail{MediaType: "series", ID: 95396, Title: "Severance", Year: 2022, Seasons: []tmdb.SeasonSummary{
		{Number: 0, Name: "Specials", EpisodeCount: 3, AirDate: "2022-01-01"},
		{Number: 1, Name: "Season 1", EpisodeCount: 9, AirDate: "2022-02-18"},
		{Number: 2, Name: "Season 2", EpisodeCount: 10, AirDate: "2025-01-17"},
		{Number: 3, Name: "Season 3", EpisodeCount: 0, AirDate: ""},
	}}
}

// severanceInLibrary puts the series in the library with season 1 complete
// and season 2 partly present.
func severanceInLibrary() *fakePresence {
	return &fakePresence{
		available: map[MediaType]map[int]bool{MediaTypeSeries: {95396: true}},
		seasons: map[string]map[int]SeasonCounts{
			fakePresenceContentID(MediaTypeSeries, 95396): {1: {Aired: 9, Have: 9}, 2: {Aired: 10, Have: 4}},
		},
	}
}

func seasonService(store *fakeStore, presence *fakePresence) *Service {
	svc := NewService(store, &fakeTMDBClient{detail: severanceDetail()}, presence)
	svc.Now = newTestService(store).Now
	svc.SetUserRepository(requestUserRepo{})
	return svc
}

func TestCreateSeriesRequestForNamedSeasons(t *testing.T) {
	store := newFakeStore()
	svc := seasonService(store, &fakePresence{})

	req, err := svc.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 95396, Title: "Severance", Seasons: []int{3, 1, 1}})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if !slices.Equal(req.Seasons, []int{1, 3}) {
		t.Fatalf("seasons = %v, want [1 3]", req.Seasons)
	}

	if _, err := svc.CreateRequest(context.Background(), testViewer(2), CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 95396, Title: "Severance", Seasons: []int{9}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown season: err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.CreateRequest(context.Background(), testViewer(2), CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: 550, Title: "Fight Club", Seasons: []int{1}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("movie with seasons: err = %v, want ErrInvalidInput", err)
	}
	// A season below 1 is refused on the seasons field, never dropped into a
	// request for every missing season.
	for _, seasons := range [][]int{{0}, {-1}, {2, 0}} {
		_, err := svc.CreateRequest(context.Background(), testViewer(2), CreateRequestInput{MediaType: MediaTypeSeries, TMDBID: 95396, Title: "Severance", Seasons: seasons})
		verr, ok := errors.AsType[*ValidationError](err)
		if !ok || verr.FieldErrors["seasons"] == "" {
			t.Fatalf("seasons %v: err = %v, want a validation error on seasons", seasons, err)
		}
	}
}

// A season request completes when its seasons are complete, not when the
// first episode of the series is scanned in.
func TestReconcileCompletesASeasonRequestOnlyWhenItsSeasonsAre(t *testing.T) {
	store := newFakeStore()
	presence := severanceInLibrary()
	store.waiting = []*Request{{ID: "r1", MediaType: MediaTypeSeries, TMDBID: 95396, Status: StatusPending, Outcome: OutcomeActive, Seasons: []int{2}}}
	svc := seasonService(store, presence)

	if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := store.requests["r1"]; got != nil && got.Status == StatusCompleted {
		t.Fatalf("request = %+v, want still pending: season 2 is partial", got)
	}

	presence.seasons[fakePresenceContentID(MediaTypeSeries, 95396)][2] = SeasonCounts{Aired: 10, Have: 10}
	if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := store.requests["r1"]; got == nil || got.Status != StatusCompleted {
		t.Fatalf("request = %+v, want completed once season 2 is", got)
	}
}

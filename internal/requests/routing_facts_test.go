package requests

import (
	"context"
	"slices"
	"testing"

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
		!slices.Equal(facts.GenreIDs, []int{16, 14}) || !slices.Equal(facts.CompanyIDs, []int{10342}) {
		t.Fatalf("facts = %+v, want the TMDB snapshot", facts)
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

package metadata

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// peopleItemRepo adds stored credits to the fake item repo.
type peopleItemRepo struct {
	*fakeItemRepo
	people []models.ItemPerson
	err    error
}

func (r *peopleItemRepo) GetPeople(context.Context, string) ([]models.ItemPerson, error) {
	return r.people, r.err
}

func TestEnrichLocalPeopleKeepsStoredCreditDetails(t *testing.T) {
	stored := models.ItemPerson{Person: models.Person{Name: "Jane Doe", TmdbID: "42", PhotoPath: "tmdb/people/42.webp"}, Kind: models.PersonKindActor, Character: "Hero"}
	repo := &peopleItemRepo{fakeItemRepo: newFakeItemRepo(), people: []models.ItemPerson{stored}}
	s := &MetadataService{itemRepo: repo}

	people := []models.ItemPerson{
		{Person: models.Person{Name: "Jane Doe"}, Kind: models.PersonKindActor},
		{Person: models.Person{Name: "New Face"}, Kind: models.PersonKindActor, Character: "Sidekick"},
	}
	if err := s.enrichLocalPeople(context.Background(), "movie-1", people); err != nil {
		t.Fatalf("enrichLocalPeople: %v", err)
	}

	if people[0].TmdbID != "42" || people[0].PhotoPath != "tmdb/people/42.webp" || people[0].Character != "Hero" {
		t.Fatalf("matched credit = %+v, want the stored IDs, photo and character", people[0])
	}
	if people[1].TmdbID != "" || people[1].Character != "Sidekick" {
		t.Fatalf("unmatched credit = %+v, want it unchanged", people[1])
	}
	if len(people) != 2 {
		t.Fatalf("credits = %d, want only the local files' two", len(people))
	}
}

func TestEnrichLocalPeopleFailsWhenStoredCreditsCannotLoad(t *testing.T) {
	repo := &peopleItemRepo{fakeItemRepo: newFakeItemRepo(), err: errors.New("database unavailable")}
	s := &MetadataService{itemRepo: repo}
	people := []models.ItemPerson{{Person: models.Person{Name: "Jane Doe"}, Kind: models.PersonKindActor}}
	if err := s.enrichLocalPeople(context.Background(), "movie-1", people); err == nil {
		t.Fatal("enrichLocalPeople succeeded without the stored credits, want an error")
	}
}

package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func authorPeople(names ...string) []models.ItemPerson {
	people := make([]models.ItemPerson, 0, len(names))
	for _, n := range names {
		people = append(people, models.ItemPerson{
			Person: models.Person{Name: n},
			Kind:   models.PersonKindAuthor,
		})
	}
	return people
}

func TestAuthorsAgree(t *testing.T) {
	cases := []struct {
		name   string
		item   string
		people []models.ItemPerson
		want   bool
	}{
		{"initials spacing", "J.R.R. Tolkien", authorPeople("J. R. R. Tolkien"), true},
		{"surname first", "Stephen King", authorPeople("King, Stephen"), true},
		{"added middle initial", "Iain Banks", authorPeople("Iain M. Banks"), true},
		{"added middle initial before particle", "Ursula Le Guin", authorPeople("Ursula K. Le Guin"), true},
		{"case", "andy weir", authorPeople("Andy Weir"), true},
		{"accents differ, surname and initial hold", "Émile Zola", authorPeople("Emile Zola"), true},
		{"different person", "Stephen King", authorPeople("Dean Koontz"), false},
		{"different person 2", "Andy Weir", authorPeople("Ernest Cline"), false},
		{"different initials and surname", "J.K. Rowling", authorPeople("J.R.R. Tolkien"), false},
		// Cyrillic initials share the same leading UTF-8 byte but are distinct
		// runes; comparing byte slices used to accept this pair.
		{"cyrillic initials", "Алексей Иванов", authorPeople("Борис Иванов"), false},
		// Absence must never count as disagreement. Most of this library is
		// missing an author on one side or the other, and treating that as a
		// conflict would reject far more good matches than bad ones.
		{"item with no author", "", authorPeople("Stephen King"), true},
		{"provider returning no credits", "Stephen King", nil, true},
		{"blank credited name", "Stephen King", authorPeople(""), true},
		{"whitespace-only item author", "   ", authorPeople("Stephen King"), true},
		// A book credited to several authors matches if any of them is ours.
		{"co-author", "Neil Gaiman", authorPeople("Terry Pratchett", "Neil Gaiman"), true},
		{"uncredited author", "Stephen King", authorPeople("Terry Pratchett", "Neil Gaiman"), false},
		// A narrator differing from the item's author is not a conflict.
		{"narrator credit is not a conflicting author", "J.R.R. Tolkien", []models.ItemPerson{{
			Person: models.Person{Name: "Rob Inglis"},
			Kind:   models.PersonKindNarrator,
		}}, true},
	}
	for _, tc := range cases {
		if got := AuthorsAgree(tc.item, tc.people); got != tc.want {
			t.Errorf("%s: AuthorsAgree(%q, %+v) = %v, want %v", tc.name, tc.item, tc.people, got, tc.want)
		}
	}
}

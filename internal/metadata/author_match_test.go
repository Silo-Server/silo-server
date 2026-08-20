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

func TestAuthorsAgreeAcceptsTheSamePersonWrittenDifferently(t *testing.T) {
	cases := []struct{ item, credited string }{
		{"J.R.R. Tolkien", "J. R. R. Tolkien"},
		{"Stephen King", "King, Stephen"},
		{"Iain Banks", "Iain M. Banks"},
		{"Ursula Le Guin", "Ursula K. Le Guin"},
		{"andy weir", "Andy Weir"},
		{"Émile Zola", "Emile Zola"}, // accents differ, surname and initial hold
	}
	for _, tc := range cases {
		if !AuthorsAgree(tc.item, authorPeople(tc.credited)) {
			t.Errorf("AuthorsAgree(%q, %q) = false, want true", tc.item, tc.credited)
		}
	}
}

func TestAuthorsAgreeRejectsADifferentPerson(t *testing.T) {
	cases := []struct{ item, credited string }{
		{"Stephen King", "Dean Koontz"},
		{"Andy Weir", "Ernest Cline"},
		{"J.K. Rowling", "J.R.R. Tolkien"},
		// Cyrillic initials share the same leading UTF-8 byte but are distinct
		// runes; comparing byte slices used to accept this pair.
		{"Алексей Иванов", "Борис Иванов"},
	}
	for _, tc := range cases {
		if AuthorsAgree(tc.item, authorPeople(tc.credited)) {
			t.Errorf("AuthorsAgree(%q, %q) = true, want false", tc.item, tc.credited)
		}
	}
}

// Absence must never count as disagreement. Most of this library is missing an
// author on one side or the other, and treating that as a conflict would reject
// far more good matches than bad ones.
func TestAuthorsAgreeTreatsMissingDataAsAgreement(t *testing.T) {
	if !AuthorsAgree("", authorPeople("Stephen King")) {
		t.Error("an item with no author should not be rejected")
	}
	if !AuthorsAgree("Stephen King", nil) {
		t.Error("a provider returning no credits should not be rejected")
	}
	if !AuthorsAgree("Stephen King", authorPeople("")) {
		t.Error("a blank credited name should not be rejected")
	}
	if !AuthorsAgree("   ", authorPeople("Stephen King")) {
		t.Error("a whitespace-only item author should not be rejected")
	}
}

// A book credited to several authors matches if any of them is ours.
func TestAuthorsAgreeMatchesAnyCreditedAuthor(t *testing.T) {
	people := authorPeople("Terry Pratchett", "Neil Gaiman")
	if !AuthorsAgree("Neil Gaiman", people) {
		t.Error("a co-author should match")
	}
	if AuthorsAgree("Stephen King", people) {
		t.Error("an uncredited author should not match")
	}
}

// Non-author credits must not be read as authorship: a narrator sharing the
// item's author name is not evidence, and a narrator differing from it is not
// a conflict.
func TestAuthorsAgreeIgnoresNonAuthorCredits(t *testing.T) {
	narratorOnly := []models.ItemPerson{{
		Person: models.Person{Name: "Rob Inglis"},
		Kind:   models.PersonKindNarrator,
	}}
	if !AuthorsAgree("J.R.R. Tolkien", narratorOnly) {
		t.Error("a narrator credit must not be treated as a conflicting author")
	}
}

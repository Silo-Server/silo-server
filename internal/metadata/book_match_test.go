package metadata

import (
	"context"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func bookResult(title, id string, year int) SearchResult {
	return SearchResult{Name: title, Year: year, ProviderIDs: map[string]string{"audiobook-metadata": id, "asin": id}}
}

// Outcomes the audiobook audit saw against live providers, and the ones the
// rules must give instead.
var audiobookRuleCases = []struct {
	name     string
	want     string
	noAuthor bool
	position int
	existing map[string]string
	results  []SearchResult
	wantID   string // "" means no match
}{
	{
		name: "series name does not stand in for volume 2",
		want: "Dungeon Crawler Carl 2",
		results: []SearchResult{
			bookResult("Dungeon Crawler Carl", "B1", 2020),
			bookResult("Carl's Doomsday Scenario: Dungeon Crawler Carl, Book 2", "B2", 2021),
		},
		wantID: "B2",
	},
	{
		name:    "series name alone is no match for volume 2",
		want:    "Mistborn 2",
		results: []SearchResult{bookResult("Mistborn", "M1", 2006), bookResult("Mistborn: A LitRPG Adventure", "M9", 2006)},
	},
	{
		name: "series suffix on the item keeps the plain title credible",
		want: "Guards! Guards!: Discworld, Book 8",
		results: []SearchResult{
			bookResult("Guards! Guards!", "G1", 2005),
		},
		wantID: "G1",
	},
	{
		name:     "series position breaks a tie for the candidate stating that volume",
		want:     "The Well of Ascension",
		position: 2,
		results: []SearchResult{
			bookResult("The Well of Ascension: A Mistborn Novel", "W0", 2008),
			bookResult("The Well of Ascension: Mistborn, Book 2", "W2", 2008),
			bookResult("The Well of Ascension: Mistborn, Book 3", "W3", 2008),
		},
		wantID: "W2",
	},
	{
		name:     "a series position in other numbering does not rule out the title",
		want:     "The Well of Ascension",
		position: 4,
		results:  []SearchResult{bookResult("The Well of Ascension: Mistborn, Book 2", "W2", 2008)},
		wantID:   "W2",
	},
	{
		name:     "a stated volume does not beat a better title",
		want:     "The Well of Ascension",
		position: 2,
		results: []SearchResult{
			bookResult("El Pozo de la Ascensión (Trilogía Original Mistborn 2) [The Well of Ascension (Mistborn 2)]", "ES", 2021),
			bookResult("The Well of Ascension", "EN", 2008),
		},
		wantID: "EN",
	},
	{
		name: "box set is not the plain title",
		want: "The Hunger Games",
		results: []SearchResult{
			bookResult("The Hunger Games (Box Set)", "H9", 2010),
			bookResult("The Hunger Games", "H1", 2008),
		},
		wantID: "H1",
	},
	{
		name:    "abridged and dramatized editions are not the plain title",
		want:    "Guards! Guards!",
		results: []SearchResult{bookResult("Guards! Guards! (Abridged)", "A1", 1995), bookResult("Guards! Guards! (Dramatized)", "D1", 2001)},
	},
	{
		name:    "a volume range is a collection",
		want:    "The Hunger Games",
		results: []SearchResult{bookResult("The Hunger Games Trilogy: Books 1-3", "T1", 2010)},
	},
	{
		name:     "a series position does not let a volume range through",
		want:     "The Hunger Games",
		position: 1,
		results:  []SearchResult{bookResult("The Hunger Games Trilogy: Books 1-3", "T1", 2010)},
	},
	{
		name:    "an item asking for the abridged edition does not take the full one",
		want:    "Guards! Guards! (Abridged)",
		results: []SearchResult{bookResult("Guards! Guards!", "G1", 2005), bookResult("Guards! Guards! (Abridged)", "A1", 1995)},
		wantID:  "A1",
	},
	{
		name:     "no author: exact title alone does not match",
		want:     "Rebecca",
		noAuthor: true,
		results:  []SearchResult{bookResult("Rebecca", "R1", 1938)},
	},
	{
		name:     "no author: a tagged ASIN matches whatever the folder title",
		want:     "ASIN Only",
		noAuthor: true,
		existing: map[string]string{"asin": "B002V1O7UG"},
		results: []SearchResult{
			bookResult("Only", "X1", 2010),
			bookResult("The Real Title", "B002V1O7UG", 2009),
		},
		wantID: "B002V1O7UG",
	},
	{
		name:     "tagged ASIN beats a higher title score",
		want:     "Project Hail Mary",
		existing: map[string]string{"asin": "P2"},
		results: []SearchResult{
			bookResult("Project Hail Mary", "P1", 2021),
			bookResult("Project Hail Mary: A Novel", "P2", 2021),
		},
		wantID: "P2",
	},
	{
		name:     "tagged ASIN whose record shares no title word does not win on the ID",
		want:     "Project Hail Mary",
		existing: map[string]string{"asin": "Z9"},
		results:  []SearchResult{bookResult("Cooking for Two", "Z9", 2021)},
	},
}

func TestAdmitSearchMatchAudiobookRules(t *testing.T) {
	for _, tt := range audiobookRuleCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AdmitSearchMatch(context.Background(), SearchMatchAdmissionRequest{
				WantTitle:           tt.want,
				Results:             tt.results,
				ExistingProviderIDs: tt.existing,
				ItemType:            "audiobook",
				ContentID:           "item",
				NoAuthor:            tt.noAuthor,
				Rules:               &BookMatchRules{SeriesPosition: tt.position},
			})
			if err != nil {
				t.Fatal(err)
			}
			gotID := ""
			if got.Status == SearchMatchAccepted {
				gotID = got.Selected.ProviderIDs["asin"]
			}
			if gotID != tt.wantID {
				t.Fatalf("matched %q (%s), want %q", gotID, got.Status, tt.wantID)
			}
		})
	}
}

// Equal candidates must resolve the same way whatever order the provider
// returned them in; the plugin's order follows goroutine completion.
func TestAdmitSearchMatchAudiobookRulesDeterministic(t *testing.T) {
	results := []SearchResult{
		bookResult("The Martian", "W1", 2014),
		bookResult("The Martian", "B1", 2014),
		bookResult("The Martian", "C1", 2014),
	}
	var picked []string
	for range 6 {
		got, err := AdmitSearchMatch(context.Background(), SearchMatchAdmissionRequest{
			WantTitle: "The Martian", Results: results, ItemType: "audiobook", Rules: &BookMatchRules{},
		})
		if err != nil {
			t.Fatal(err)
		}
		picked = append(picked, got.Selected.ProviderIDs["asin"])
		slices.Reverse(results)
		results = append(results[1:], results[0])
	}
	for _, id := range picked {
		if id != picked[0] {
			t.Fatalf("picks differ by result order: %v", picked)
		}
	}
}

func TestCreditsAgree(t *testing.T) {
	people := func(kind models.PersonKind, names ...string) []models.ItemPerson {
		var out []models.ItemPerson
		for _, n := range names {
			out = append(out, models.ItemPerson{Person: models.Person{Name: n}, Kind: kind})
		}
		return out
	}
	tests := []struct {
		name   string
		item   []string
		people []models.ItemPerson
		kind   models.PersonKind
		want   bool
	}{
		{"same narrator, different punctuation", []string{"R. C. Bray"}, people(models.PersonKindNarrator, "R.C. Bray"), models.PersonKindNarrator, true},
		{"other narrator", []string{"R. C. Bray"}, people(models.PersonKindNarrator, "Wil Wheaton"), models.PersonKindNarrator, false},
		{"other narrator, abridged edition", []string{"Nigel Planer"}, people(models.PersonKindNarrator, "Tony Robinson"), models.PersonKindNarrator, false},
		{"joined tag holds the narrator", []string{"Michael Kramer, Kate Reading"}, people(models.PersonKindNarrator, "Kate Reading"), models.PersonKindNarrator, true},
		{"surname first", []string{"Bray, R. C."}, people(models.PersonKindNarrator, "R. C. Bray"), models.PersonKindNarrator, true},
		{"no item narrator", nil, people(models.PersonKindNarrator, "Wil Wheaton"), models.PersonKindNarrator, true},
		{"no record narrator", []string{"R. C. Bray"}, people(models.PersonKindAuthor, "Andy Weir"), models.PersonKindNarrator, true},
		{"bare surname is not any author of that name", []string{"King"}, people(models.PersonKindAuthor, "Owen King"), models.PersonKindAuthor, false},
		{"single name matches itself", []string{"Homer"}, people(models.PersonKindAuthor, "Homer"), models.PersonKindAuthor, true},
		{"middle initial", []string{"Iain Banks"}, people(models.PersonKindAuthor, "Iain M. Banks"), models.PersonKindAuthor, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CreditsAgree(tt.item, tt.people, tt.kind); got != tt.want {
				t.Fatalf("CreditsAgree = %v, want %v", got, tt.want)
			}
		})
	}
}

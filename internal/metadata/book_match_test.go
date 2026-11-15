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
	bare     string
	volume   string // the VolumeTitle a series prefix gives
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
		name:    "a number in the title is not a volume: Slaughterhouse-Five",
		want:    "Slaughterhouse-Five",
		results: []SearchResult{bookResult("Slaughterhouse-Five, Book 1", "SF", 2000)},
		wantID:  "SF",
	},
	{
		name:    "a number in the title is not a volume: Catch-22",
		want:    "Catch-22",
		results: []SearchResult{bookResult("Catch-22, Book 1", "C22", 2000)},
		wantID:  "C22",
	},
	{
		name:    "a number in the title is not a volume: Fahrenheit 451",
		want:    "Fahrenheit 451",
		results: []SearchResult{bookResult("Fahrenheit 451, Book 1", "F451", 2000)},
		wantID:  "F451",
	},
	{
		// "1984, Book 1" itself scores at the threshold (one shared word of
		// three), as on main; VolumesContradict covers its volume.
		name:    "a number in the title is not a volume: 1984",
		want:    "1984",
		results: []SearchResult{bookResult("1984", "N84", 2000), bookResult("1984, Book 2", "N2", 2000)},
		wantID:  "N84",
	},
	{
		name: "a number in the series name does not hide the trailing volume",
		want: "The 39 Clues 2",
		results: []SearchResult{
			bookResult("The 39 Clues, Book 1", "C1", 2008),
			bookResult("The 39 Clues", "C0", 2008),
			bookResult("One False Note: The 39 Clues, Book 2", "C2", 2008),
		},
		wantID: "C2",
	},
	{
		name:    "Area 51 2 is not Area 51, Book 1",
		want:    "Area 51 2",
		results: []SearchResult{bookResult("Area 51, Book 1", "A1", 1997)},
	},
	{
		name: "an alias naming another volume does not establish the match",
		want: "Dungeon Crawler Carl 2",
		results: []SearchResult{{
			Name: "The Gate of the Feral Gods", ProviderIDs: map[string]string{"asin": "G4"},
			TitleAliases: []TitleAlias{{Title: "Dungeon Crawler Carl, Book 4"}},
		}},
	},
	{
		name: "an alias naming the item's volume establishes the match",
		want: "Dungeon Crawler Carl 2",
		results: []SearchResult{{
			Name: "Carl's Doomsday Scenario", ProviderIDs: map[string]string{"asin": "D2"},
			TitleAliases: []TitleAlias{{Title: "Dungeon Crawler Carl, Book 2"}},
		}},
		wantID: "D2",
	},
	{
		name: "a primary title in another series' numbering passes when an alias carries the item's",
		want: "Guards! Guards!: Discworld, Book 8",
		results: []SearchResult{{
			Name: "Guards! Guards!: City Watch, Book 1", ProviderIDs: map[string]string{"asin": "GW"},
			TitleAliases: []TitleAlias{{Title: "Guards! Guards!: Discworld, Book 8"}},
		}},
		wantID: "GW",
	},
	{
		name: "an unrelated alias that only carries the item's number does not override",
		want: "Guards! Guards!: Discworld, Book 8",
		results: []SearchResult{{
			Name: "Guards! Guards!: City Watch, Book 1", ProviderIDs: map[string]string{"asin": "GW"},
			TitleAliases: []TitleAlias{{Title: "Ringworld, Book 8"}},
		}},
	},
	{
		name: "a primary title in another numbering without an agreeing alias is rejected",
		want: "Guards! Guards!: Discworld, Book 8",
		results: []SearchResult{{
			Name: "Guards! Guards!: City Watch, Book 1", ProviderIDs: map[string]string{"asin": "GW"},
		}},
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
		name: "a decimal volume is neither of its digits",
		want: "Series Name 0.5",
		results: []SearchResult{
			bookResult("Other Title: Series Name, Book 5", "S5", 2020),
			bookResult("Prequel Title: Series Name, Book 0.5", "S05", 2020),
		},
		wantID: "S05",
	},
	{
		name:    "a decimal volume is no match for the whole volume",
		want:    "Series Name 0.5",
		results: []SearchResult{bookResult("Other Title: Series Name, Book 5", "S5", 2020)},
	},
	{
		name:     "a series-prefixed title is scored by its bare title",
		want:     "Series Name 3 - Gone",
		bare:     "Gone",
		volume:   "Gone, Book 3",
		position: 3,
		results:  []SearchResult{bookResult("Gone", "G3", 2020)},
		wantID:   "G3",
	},
	{
		name:     "a series-prefixed title keeps its volume",
		want:     "Series Name 3 - Gone",
		bare:     "Gone",
		volume:   "Gone, Book 3",
		position: 3,
		results:  []SearchResult{bookResult("Gone: Series Name, Book 4", "G4", 2020)},
	},
	{
		name:     "a series-prefixed title still matches a provider title that keeps the prefix",
		want:     "Series Name 3 - Gone",
		bare:     "Gone",
		volume:   "Gone, Book 3",
		position: 3,
		results:  []SearchResult{bookResult("Series Name 3 - Gone", "GP", 2020)},
		wantID:   "GP",
	},
	{
		name:     "a number in the bare title does not replace the series prefix's volume",
		want:     "Series Name 3 - Room 101",
		bare:     "Room 101",
		volume:   "Room 101, Book 3",
		position: 3,
		results: []SearchResult{
			bookResult("Room 101: Series Name, Book 4", "R4", 2020),
			bookResult("Room 101: Series Name, Book 3", "R3", 2020),
		},
		wantID: "R3",
	},
	{
		name:     "a number in the bare title does not let another volume through",
		want:     "Series Name 3 - Room 101",
		bare:     "Room 101",
		volume:   "Room 101, Book 3",
		position: 3,
		results:  []SearchResult{bookResult("Room 101: Series Name, Book 4", "R4", 2020)},
	},
	{
		name:    "a series name holding the volume's number is no match for that volume",
		want:    "Zone 3, Book 3",
		results: []SearchResult{bookResult("Zone 3", "Z0", 2020)},
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
				Rules:               &BookMatchRules{SeriesPosition: tt.position, BareTitle: tt.bare, VolumeTitle: tt.volume},
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
		{"given name spaced differently", []string{"Jo Anne Smith"}, people(models.PersonKindAuthor, "JoAnne Smith"), models.PersonKindAuthor, true},
		{"surname spaced differently", []string{"Mary Van Dyke"}, people(models.PersonKindAuthor, "Mary VanDyke"), models.PersonKindAuthor, true},
		{"one-word tag spaced differently", []string{"JoAnne"}, people(models.PersonKindAuthor, "Jo Anne"), models.PersonKindAuthor, true},
		{"initials with and without spaces", []string{"J.R. Smith"}, people(models.PersonKindAuthor, "J. R. Smith"), models.PersonKindAuthor, true},
		{"initials run together", []string{"JR Smith"}, people(models.PersonKindAuthor, "J. R. Smith"), models.PersonKindAuthor, true},
		{"different given names, same surname", []string{"Jo Smith"}, people(models.PersonKindAuthor, "Anne Smith"), models.PersonKindAuthor, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CreditsAgree(tt.item, tt.people, tt.kind); got != tt.want {
				t.Fatalf("CreditsAgree = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVolumesContradict(t *testing.T) {
	tests := []struct {
		want, record string
		contradicts  bool
	}{
		{"Dungeon Crawler Carl 2", "Dungeon Crawler Carl, Book 1", true},
		{"Dungeon Crawler Carl 2", "Carl's Doomsday Scenario: Dungeon Crawler Carl, Book 2", false},
		{"Guards! Guards!: Discworld, Book 8", "Guards! Guards!", false},
		{"Slaughterhouse-Five", "Slaughterhouse-Five, Book 1", false},
		{"Catch-22", "Catch-22, Book 1", false},
		{"Fahrenheit 451", "Fahrenheit 451, Book 1", false},
		{"1984", "1984, Book 1", false},
		{"The 39 Clues 2", "The 39 Clues, Book 1", true},
		{"Area 51 2", "Area 51, Book 1", true},
		{"Series Name 0.5", "Other Title: Series Name, Book 5", true},
		{"Series Name 0.5", "Other Title: Series Name, Book 0", true},
		{"Series Name 0.5", "Prequel Title: Series Name, Book 0.5", false},
		{"Series Name 2.5", "Other Title: Series Name, Book 2", true},
		{"Series Name, Book 0.50", "Series Name, Book 0.5", false},
		{"Series Name, Book 0.5", "Series Name, Book 5", true},
		{"Series Name #1.5", "Series Name, Book 1", true},
		{"Engine 3.0", "Engine 3.0: A Novel", false},
	}
	for _, tt := range tests {
		if got := VolumesContradict(tt.want, tt.record); got != tt.contradicts {
			t.Errorf("VolumesContradict(%q, %q) = %v, want %v", tt.want, tt.record, got, tt.contradicts)
		}
	}
}

func TestRecordVolumeContradicts(t *testing.T) {
	tests := []struct {
		name        string
		want        string
		titles      []string
		contradicts bool
	}{
		{"only another volume", "Dungeon Crawler Carl 2", []string{"The Gate of the Feral Gods", "Dungeon Crawler Carl, Book 4"}, true},
		{"one numbering agrees", "Guards! Guards!: Discworld, Book 8", []string{"Guards! Guards!", "Guards! Guards!: City Watch, Book 1", "Guards! Guards!: Discworld, Book 8"}, false},
		{"item states no volume", "Guards! Guards!", []string{"Guards! Guards!", "Guards! Guards!: City Watch, Book 1"}, false},
		{"same volume", "Dungeon Crawler Carl 2", []string{"Carl's Doomsday Scenario", "Dungeon Crawler Carl, Book 2"}, false},
	}
	for _, tt := range tests {
		if got := RecordVolumeContradicts(tt.want, tt.titles); got != tt.contradicts {
			t.Errorf("%s: RecordVolumeContradicts = %v, want %v", tt.name, got, tt.contradicts)
		}
	}
}

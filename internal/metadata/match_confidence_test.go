package metadata

import "testing"

// The pairs below are real: 20 unidentified production audiobooks were queried
// against the iTunes audiobook search on 2026-07-27, and these are the titles
// it returned. 19 of 20 came back with something and roughly a quarter of those
// were wrong, which is what motivated this gate. Keeping the actual pairs means
// the calibration is anchored to observed provider behavior rather than to
// invented examples.
var productionPairs = []struct {
	name    string
	want    string // title as it exists in our library
	got     string // top result iTunes returned
	accept  bool
	because string
}{
	// --- correct matches that MUST survive the gate ---
	{
		name:    "identical but for edition decoration",
		want:    "Mother of Storms",
		got:     "Mother of Storms (Unabridged)",
		accept:  true,
		because: "(Unabridged) is an edition decoration, not an identity difference",
	},
	{
		name:   "subtitle plus decoration",
		want:   "The Face: A Novel",
		got:    "The Face: A Novel (Unabridged)",
		accept: true,
	},
	{
		name:    "series reordered into a parenthetical",
		want:    "Sky Brooks World: Ethan 6 - Darkness Revealed",
		got:     "Darkness Revealed (Sky Brooks World: Ethan, Book 6)",
		accept:  true,
		because: "same words, same volume, only the arrangement differs",
	},
	{
		name:    "author prefix added by the provider",
		want:    "Op-Center 4 - Acts of War",
		got:     "Tom Clancy's Op-Center #4: Acts of War",
		accept:  true,
		because: "volume 4 agrees; the extra author words must not sink it",
	},
	{
		name:   "provider truncates our subtitle",
		want:   "Frankly, We Did Win This Election: The Inside Story of How Trump Lost",
		got:    "Frankly, We Did Win This Election",
		accept: true,
	},
	{
		name:   "series marker moves, volume agrees",
		want:   "Phoenix Brothers Series 2 - More Than a Phoenix",
		got:    "More than a Phoenix (Phoenix Brothers Book 2)",
		accept: true,
	},
	{
		name:   "volume agrees across differing notation",
		want:   "Ravenloft: The Covenant 5 - Scholar of Decay",
		got:    "Scholar of Decay: Ravenloft: The Covenant",
		accept: true,
	},

	// --- wrong matches the gate MUST reject ---
	{
		name:    "same series, different volume",
		want:    "The OP MC 8: God of Winning",
		got:     "God of Winning: The OP MC, Book 1",
		accept:  false,
		because: "nearly every word matches but book 8 is not book 1",
	},
	{
		name:   "same series, different volume, reversed direction",
		want:   "Legend of Randidly Ghosthound 1 - The Legend of Randidly Ghosthound",
		got:    "The Legend of Randidly Ghosthound 8: A LitRPG Adventure",
		accept: false,
	},
	{
		name:   "unrelated title",
		want:   "Looking for a Miracle: Weeping Icons, Relics and Healing Cures",
		got:    "Lucky You: A Novel (Abridged)",
		accept: false,
	},
	{
		name:    "completely unrelated subject",
		want:    "Star Force Origins - 002-Integration",
		got:     "The Achilles Trap: Saddam Hussein, the CIA and the Origins of America's Invasion of Iraq",
		accept:  false,
		because: "shares only the common word 'origins'",
	},
	{
		name:    "two different boxed sets sharing only boilerplate",
		want:    "All the Lies 1-3 - All the Lies: The Complete Trilogy",
		got:     "The Sentinel: The Complete Jane Harper Trilogy: The Jane Harper Trilogy, Books 1-3 (Unabridged)",
		accept:  false,
		because: "both are 'Books 1-3' sets, so 'the/complete/trilogy/1/3' overlap without sharing an identity",
	},
	{
		name:    "series name matches but the volume is a different book",
		want:    "Storm Princess Saga 2 - The Princess Must Strike",
		got:     "The Princess Must Die: Storm Princess Saga, Book 1 (Unabridged)",
		accept:  false,
		because: "Must Strike is not Must Die, and volume 2 is not volume 1",
	},
	{
		name:    "generic one-word title against an unrelated long one",
		want:    "Bitcoin",
		got:     "Bitcoin: Hard Money You Can't F*ck With: Why Bitcoin Will Be the Next Global Reserve Currency (Unabridged)",
		accept:  false,
		because: "a bare common noun cannot identify a specific book",
	},
}

func TestBestMatchOnProductionPairs(t *testing.T) {
	for _, tc := range productionPairs {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := BestMatch(tc.want, []SearchResult{{Name: tc.got}})
			if ok != tc.accept {
				t.Errorf("BestMatch(%q, %q) accepted=%v, want %v (score %.2f)\n  %s",
					tc.want, tc.got, ok, tc.accept, TitleScore(tc.want, tc.got), tc.because)
			}
			if ok && got.Name != tc.got {
				t.Errorf("BestMatch returned %q, want %q", got.Name, tc.got)
			}
		})
	}
}

func TestBestMatchPicksHighestScoringCandidate(t *testing.T) {
	results := []SearchResult{
		{Name: "Acts of War: Something Else Entirely"},
		{Name: "Tom Clancy's Op-Center #4: Acts of War"},
		{Name: "Unrelated Book About Gardening"},
	}
	got, ok := BestMatch("Op-Center 4 - Acts of War", results)
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Name != "Tom Clancy's Op-Center #4: Acts of War" {
		t.Errorf("picked %q, want the volume-4 match", got.Name)
	}
}

// Years date an edition; they must not be read as volume numbers, or every
// title carrying a year would collide with every other.
func TestYearsAreNotTreatedAsVolumes(t *testing.T) {
	if _, ok := titleVolume("Best American Essays 2019"); ok {
		t.Error("a year was parsed as a volume number")
	}
}

// Normalisation must not be ASCII-only. An earlier version stripped via
// [^a-z0-9], which reduced non-Latin titles to the empty string -- an identical
// Japanese title then scored 0 against itself and was rejected, and accented
// Latin titles were shredded. That would have been a hard regression for
// non-English content, which under the old blind results[0] path matched by
// accident because nothing was checked at all.
func TestNonLatinTitlesSurviveNormalisation(t *testing.T) {
	for _, title := range []string{"進撃の巨人", "Мастер и Маргарита", "Blåbærsyltetøy"} {
		if got := normaliseTitle(title); got == "" {
			t.Errorf("normaliseTitle(%q) = %q, want the title's characters preserved", title, got)
		}
		if s := TitleScore(title, title); s != 1 {
			t.Errorf("TitleScore(%q, itself) = %.2f, want 1", title, s)
		}
		if _, ok := BestMatch(title, []SearchResult{{Name: title}}); !ok {
			t.Errorf("BestMatch(%q) rejected an identical title", title)
		}
	}
}

// Folding must not fire on single letters that are initials or words: a Roman
// numeral reading of "X" or "I" would rewrite real titles into nonsense.
func TestSingleLetterIsNotFoldedAsANumeral(t *testing.T) {
	if got := normaliseTitle("Malcolm X"); got != "malcolm x" {
		t.Errorf("normaliseTitle(%q) = %q, want the letter left alone", "Malcolm X", got)
	}
	if _, ok := BestMatch("Malcolm X", []SearchResult{{Name: "Malcolm 10"}}); ok {
		t.Error("a single letter was read as a Roman numeral")
	}
}

// Year breaks ties only. It must never override a clearly better title, since
// for books it is weak evidence -- an audiobook edition of a 1994 novel is
// routinely dated by its recording decades later.
func TestYearBreaksTiesButNeverOverridesTitle(t *testing.T) {
	tied := []SearchResult{
		{Name: "The Silent Patient", Year: 2019},
		{Name: "The Silent Patient", Year: 1975},
	}
	got, ok := BestMatchYear("The Silent Patient", 2019, tied)
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Year != 2019 {
		t.Errorf("tie broken to year %d, want 2019", got.Year)
	}

	// A far-off year must not beat a better title.
	mixed := []SearchResult{
		{Name: "Something Else Entirely", Year: 2019},
		{Name: "The Silent Patient", Year: 1975},
	}
	got, ok = BestMatchYear("The Silent Patient", 2019, mixed)
	if !ok || got.Name != "The Silent Patient" {
		t.Errorf("year overrode the better title: got %q", got.Name)
	}

	// An unknown year on either side must not decide anything.
	if _, ok := BestMatchYear("The Silent Patient", 0, tied); !ok {
		t.Error("an unknown wanted year should not prevent a match")
	}
}

func TestMatchThresholdOverride(t *testing.T) {
	t.Setenv("SILO_METADATA_MATCH_MIN_SCORE", "0.95")
	if _, ok := BestMatch("Op-Center 4 - Acts of War",
		[]SearchResult{{Name: "Tom Clancy's Op-Center #4: Acts of War"}}); ok {
		t.Error("a 0.90 match was accepted against a 0.95 threshold")
	}

	for _, bad := range []string{"0", "5", "-1", "abc", "", "NaN", "+Inf", "-Inf"} {
		t.Setenv("SILO_METADATA_MATCH_MIN_SCORE", bad)
		if got := matchThreshold(); got != minTitleScore {
			t.Errorf("threshold %q = %.2f, want the default %.2f (bad values must be ignored)", bad, got, minTitleScore)
		}
	}
}

// Two providers can each clear the bar while naming different books, so a
// caller that merges both ends up with IDs for two works and no way to tell
// which is right. AgreesWith is what stops that.
func TestAgreesWithSeparatesProviderAnswers(t *testing.T) {
	if !AgreesWith("Mother of Storms", "Mother of Storms (Unabridged)") {
		t.Error("two spellings of the same title should agree")
	}
	if AgreesWith("The OP MC 8: God of Winning", "God of Winning: The OP MC, Book 1") {
		t.Error("different volumes of one series must not agree")
	}
	if AgreesWith("Mother of Storms", "The Good Mothers") {
		t.Error("unrelated titles must not agree")
	}
}

// TestBestMatchAcceptance holds the BestMatch accept/reject cases outside the
// production pairs.
func TestBestMatchAcceptance(t *testing.T) {
	names := func(names ...string) []SearchResult {
		out := make([]SearchResult, 0, len(names))
		for _, name := range names {
			out = append(out, SearchResult{Name: name})
		}
		return out
	}
	tests := []struct {
		name    string
		want    string
		results []SearchResult
		accept  bool
	}{
		{name: "nil results", want: "Mother of Storms", results: nil},
		{name: "empty results", want: "Mother of Storms", results: []SearchResult{}},
		{name: "empty wanted title", want: "", results: names("Mother of Storms")},
		{name: "blank candidate name", want: "Mother of Storms", results: names("   ")},
		// A provider that leaves Name empty but fills OriginalTitle is still
		// usable, and a confirmed alias can rescue a regional spelling.
		{name: "OriginalTitle used when Name is empty", want: "Mother of Storms",
			results: []SearchResult{{OriginalTitle: "Mother of Storms"}}, accept: true},
		{name: "confirmed alias may match", want: "Mother of Storms", results: []SearchResult{{
			Name: "Sturmmutter", TitleAliases: []TitleAlias{{Title: "Mother of Storms", Kind: "original"}},
		}}, accept: true},
		{name: "different boxed-set ranges", want: "Dragon Saga Books 1-3", results: names("Dragon Saga Books 1-4")},
		{name: "two different Japanese titles", want: "進撃の巨人", results: names("ドラゴンボール")},
		{name: "two different Russian titles", want: "Мастер и Маргарита", results: names("Преступление и наказание")},
		// A volume written as a Roman numeral agrees with the same volume in digits.
		{name: "Book II matches Book 2", want: "The Dark Tower Book II", results: names("The Dark Tower Book 2"), accept: true},
		{name: "decomposed spelling of an identical title", want: "Caf\u00e9", results: names("Cafe\u0301"), accept: true},
		// A volume-less alias must not rescue a primary title whose volume
		// contradicts the item's: aliases are often the bare series name, and
		// accepting through one persists IDs for a different book.
		{name: "wrong-volume primary with a volume-less alias", want: "Dungeon In My Closet 2", results: []SearchResult{{
			Name: "Dungeon In My Closet, Book 5", TitleAliases: []TitleAlias{{Title: "Dungeon In My Closet"}},
		}}},
		// The alias path still rescues a primary that differs only textually.
		{name: "alias rescue for a translated title", want: "Mother of Storms", results: []SearchResult{{
			Name: "Sturmmutter", TitleAliases: []TitleAlias{{Title: "Mother of Storms"}},
		}}, accept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := BestMatch(tt.want, tt.results); ok != tt.accept {
				t.Errorf("BestMatch(%q, %+v) accepted=%v, want %v", tt.want, tt.results, ok, tt.accept)
			}
		})
	}
}

// TestTitleScoreBands holds the TitleScore cases that pin a score to zero, one,
// or one side of minTitleScore.
func TestTitleScoreBands(t *testing.T) {
	const (
		zero = iota
		nonZero
		one
		belowMin
		atLeastMin
	)
	tests := []struct {
		name string
		a, b string
		band int
	}{
		// Short titles are the containment rule's failure mode: "Bitcoin" appears
		// inside many unrelated audiobook titles.
		{"short title against a long unrelated one", "Bitcoin", "Bitcoin Billionaires: A True Story of Genius, Betrayal and Redemption", belowMin},
		{"short exact title against its own edition", "Bitcoin", "Bitcoin (Unabridged)", atLeastMin},
		// Identical but for the volume: overlap is maximal, yet these are
		// different books. A missing volume on one side is not a disagreement.
		{"volume mismatch", "Dungeon In My Closet 2", "Dungeon In My Closet 5", zero},
		{"volume absent on one side", "Dungeon In My Closet 2", "Dungeon In My Closet", nonZero},
		{"volume range mismatch", "Dragon Saga Books 1-3", "Dragon Saga Books 1-4", zero},
		{"equivalent dash spellings", "Dragon Saga Books 1–3", "Dragon Saga Books 1-3", one},
		// Providers and rippers disagree on numeral form freely. Before folding
		// these, "Slaughterhouse 5" vs "Slaughterhouse-Five" scored exactly at the
		// threshold and a "Part II" volume never agreed with "Part 2".
		{"numeral: word five", "Slaughterhouse 5", "Slaughterhouse-Five", atLeastMin},
		{"numeral: roman IV", "Star Wars: Episode IV", "Star Wars: Episode 4", atLeastMin},
		{"numeral: roman II", "The Dark Tower Part II", "The Dark Tower Part 2", atLeastMin},
		{"numeral: word eleven", "Ocean's 11", "Ocean's Eleven", atLeastMin},
		{"roman volume disagrees with a different digit", "The Dark Tower Book II", "The Dark Tower Book 3", zero},
		// Before NFC normalisation, a decomposed accent (e + U+0301) vanished in
		// the punctuation strip while the composed form kept its letter.
		{"composed and decomposed spellings", "Caf\u00e9", "Cafe\u0301", one},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := TitleScore(tt.a, tt.b)
			var ok bool
			switch tt.band {
			case zero:
				ok = s == 0
			case nonZero:
				ok = s != 0
			case one:
				ok = s == 1
			case belowMin:
				ok = s < minTitleScore
			case atLeastMin:
				ok = s >= minTitleScore
			}
			if !ok {
				t.Errorf("TitleScore(%q, %q) = %.2f, outside the wanted band %d (minTitleScore %.2f)", tt.a, tt.b, s, tt.band, minTitleScore)
			}
		})
	}
}

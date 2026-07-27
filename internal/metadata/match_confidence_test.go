package metadata

import "testing"

// The pairs below are real: 20 unidentified production audiobooks were queried
// against the iTunes audiobook search on 2026-07-27, and these are the titles
// it returned. 19 of 20 came back with something and roughly a quarter of those
// were wrong, which is what motivated this gate. Keeping the actual pairs means
// the calibration is anchored to observed provider behaviour rather than to
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
		name:    "shares only boilerplate words",
		want:    "All the Lies 1-3 - All the Lies: The Complete Collection",
		got:     "The Sentinel: The Complete Jane Harper Collection",
		accept:  false,
		because: "'the complete collection' is store boilerplate, not identity",
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

// The threshold is only meaningful if the two populations actually separate.
// Asserting the gap directly means a future tweak that narrows it fails here
// rather than silently letting mismatches through in production.
func TestProductionPairsSeparateAroundTheThreshold(t *testing.T) {
	worstAccept, bestReject := 1.0, 0.0

	for _, tc := range productionPairs {
		score := TitleScore(tc.want, tc.got)
		if tc.accept {
			if score < worstAccept {
				worstAccept = score
			}
			continue
		}
		if score > bestReject {
			bestReject = score
		}
	}

	if worstAccept <= bestReject {
		t.Fatalf("populations overlap: worst correct match scores %.2f, best wrong match scores %.2f",
			worstAccept, bestReject)
	}
	if worstAccept < minTitleScore {
		t.Errorf("a correct match scores %.2f, below the %.2f threshold", worstAccept, minTitleScore)
	}
	if bestReject >= minTitleScore {
		t.Errorf("a wrong match scores %.2f, at or above the %.2f threshold", bestReject, minTitleScore)
	}
	t.Logf("separation: correct >= %.2f, wrong <= %.2f, threshold %.2f", worstAccept, bestReject, minTitleScore)
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

func TestBestMatchRejectsEmptyAndUnusableResults(t *testing.T) {
	if _, ok := BestMatch("Mother of Storms", nil); ok {
		t.Error("nil results must not match")
	}
	if _, ok := BestMatch("Mother of Storms", []SearchResult{}); ok {
		t.Error("empty results must not match")
	}
	if _, ok := BestMatch("", []SearchResult{{Name: "Mother of Storms"}}); ok {
		t.Error("an empty wanted title must not match")
	}
	if _, ok := BestMatch("Mother of Storms", []SearchResult{{Name: "   "}}); ok {
		t.Error("a blank candidate name must not match")
	}
}

// A provider that leaves Name empty but fills OriginalTitle should still be
// usable, and a confirmed alias should be able to rescue a regional spelling.
func TestBestMatchFallsBackToOriginalTitleAndAliases(t *testing.T) {
	byOriginal := []SearchResult{{OriginalTitle: "Mother of Storms"}}
	if _, ok := BestMatch("Mother of Storms", byOriginal); !ok {
		t.Error("OriginalTitle should be used when Name is empty")
	}

	byAlias := []SearchResult{{
		Name:         "Sturmmutter",
		TitleAliases: []TitleAlias{{Title: "Mother of Storms", Kind: "original"}},
	}}
	if _, ok := BestMatch("Mother of Storms", byAlias); !ok {
		t.Error("a confirmed alias should be allowed to match")
	}
}

// Short titles are the containment rule's failure mode: "Bitcoin" appears
// inside many unrelated audiobook titles. The gate should not accept on
// containment alone below a length floor.
func TestShortTitlesDoNotMatchOnContainmentAlone(t *testing.T) {
	if s := TitleScore("Bitcoin", "Bitcoin Billionaires: A True Story of Genius, Betrayal and Redemption"); s >= minTitleScore {
		t.Errorf("short title matched a long unrelated one on containment (score %.2f)", s)
	}
	// The same title against a genuinely close answer should still work.
	if s := TitleScore("Bitcoin", "Bitcoin (Unabridged)"); s < minTitleScore {
		t.Errorf("short exact title failed to match its own edition (score %.2f)", s)
	}
}

func TestVolumeDisagreementIsFatalRegardlessOfOverlap(t *testing.T) {
	// Identical but for the volume: overlap is maximal, yet these are
	// different books.
	if s := TitleScore("Dungeon In My Closet 2", "Dungeon In My Closet 5"); s != 0 {
		t.Errorf("volume mismatch scored %.2f, want 0", s)
	}
	// A missing volume on one side is not a disagreement.
	if s := TitleScore("Dungeon In My Closet 2", "Dungeon In My Closet"); s == 0 {
		t.Error("absent volume on one side must not be treated as a mismatch")
	}
}

// Years date an edition; they must not be read as volume numbers, or every
// title carrying a year would collide with every other.
func TestYearsAreNotTreatedAsVolumes(t *testing.T) {
	if _, ok := titleVolume("Best American Essays 2019"); ok {
		t.Error("a year was parsed as a volume number")
	}
}

package metadata

import (
	"strings"
	"testing"
	"time"
)

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

// normaliseTitle must stay linear in a title's length. Titles from a file's
// tags reach it with no length limit, and checking each decimal against the
// whole text before it once took seconds on a long title full of decimals;
// at this length that version takes well over the bound.
func TestNormaliseTitleIsLinearOnManyDecimals(t *testing.T) {
	title := strings.Repeat("1.23 x ", 10000)
	start := time.Now()
	normaliseTitle(title)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("normaliseTitle took %v on a %d-byte title", elapsed, len(title))
	}
}

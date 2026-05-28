package metadata

import (
	"testing"
)

func TestSelectInitialMatchCandidate_LoneResultYearMatchBelow70(t *testing.T) {
	// Exact title, matching year, NO sources, NO provider IDs => score 45+20 = 65 (<70).
	// Old behavior rejected this (single candidate <70); the new rule accepts it.
	hints := &MatchHints{Title: "1201", Year: 1993, Type: "movie"}
	cands := []MatchCandidate{{Title: "1201", Year: 1993, ContentType: "movie"}}
	got, ok := selectInitialMatchCandidate(hints, cands, nil)
	if !ok || got == nil || got.Title != "1201" {
		t.Fatalf("expected lone year-matching result to be accepted, got ok=%v cand=%+v", ok, got)
	}
}

func TestSelectInitialMatchCandidate_SameShowAcrossTwoSources(t *testing.T) {
	// Same title+year returned once per source (TVDB-only and TMDB-only, no shared ID
	// so they were NOT merged). Old behavior: tie-break bails -> nil. New: accept best.
	hints := &MatchHints{Title: "Blue Lock", Year: 2022, Type: "series"}
	cands := []MatchCandidate{
		{Title: "Blue Lock", Year: 2022, ContentType: "series", Sources: []string{"tvdb"}, ProviderIDs: map[string]string{"tvdb": "404404"}},
		{Title: "Blue Lock", Year: 2022, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "120089"}},
	}
	got, ok := selectInitialMatchCandidate(hints, cands, nil)
	if !ok || got == nil {
		t.Fatalf("expected same-show-across-sources to be accepted, got ok=%v cand=%+v", ok, got)
	}
}

func TestSelectInitialMatchCandidate_LoneResultYearMismatchStillRejected(t *testing.T) {
	// Exact title but year mismatch, one source => score 45+12 = 57 (in [55,70), no year bonus).
	// Year does NOT corroborate, so the new rule must NOT fire; single candidate <70 => reject.
	hints := &MatchHints{Title: "1201", Year: 1993, Type: "movie"}
	cands := []MatchCandidate{{Title: "1201", Year: 1990, ContentType: "movie", Sources: []string{"tmdb"}}}
	if got, ok := selectInitialMatchCandidate(hints, cands, nil); ok {
		t.Fatalf("expected year-mismatch lone result to be rejected, got cand=%+v", got)
	}
}

func TestSelectInitialMatchCandidate_CorroborationRequiresHintTitleCoherence(t *testing.T) {
	// Same-year, multi-source candidate scores above the 55 floor via provider
	// evidence, but the title is not coherent with the scanner hint. The lone
	// result rule must not rescue it just because the year/source evidence is
	// strong enough to reach the corroboration branch.
	hints := &MatchHints{Title: "Hotel Transylvania Puppy!", Year: 2017, Type: "movie"}
	cands := []MatchCandidate{
		{
			Title:       "Puppy!",
			Year:        2017,
			ContentType: "movie",
			Sources:     []string{"tmdb", "tvdb", "imdb"},
			ProviderIDs: map[string]string{"tmdb": "222"},
		},
	}
	if got, ok := selectInitialMatchCandidate(hints, cands, []string{"tmdb", "tvdb", "imdb"}); ok {
		t.Fatalf("unrelated same-year candidate must not be auto-accepted, got %+v", got)
	}
}

func TestSelectInitialMatchCandidate_TwoDifferentShowsUnchanged(t *testing.T) {
	// Two genuinely different shows that BOTH score >=55 (so the new rule IS evaluated,
	// not short-circuited by the <55 floor): candidatesAreSingleDistinctShow must return
	// false (titles differ), so the new rule does NOT fire and behavior falls through to
	// the existing tie-break (which returns nil here because DetailScore is 0). Guards
	// against over-accepting distinct results.
	//
	// Each candidate shares 7 tokens with the hint plus one distinct trailing word, so
	// each is coherent with the hint (Jaccard 7/8 = 0.875 >= 0.85 => sim 0.8 => +28) and
	// scores 28 + 20(year) + 24(2 sources) + 5 + 1(1 id) = 78 (>=55, reaches the guard).
	// The two candidates differ from each other (Jaccard 7/9 = 0.78 < 0.85 => sim 0), so
	// candidatesAreSingleDistinctShow returns false. Equal scores => gap 0 < 15 => tie-break.
	hints := &MatchHints{Title: "The Real History of the World War", Year: 2010, Type: "series"}
	cands := []MatchCandidate{
		{Title: "The Real History of the World War Europe", Year: 2010, ContentType: "series", Sources: []string{"tvdb", "tmdb"}, ProviderIDs: map[string]string{"tvdb": "1"}},
		{Title: "The Real History of the World War Pacific", Year: 2010, ContentType: "series", Sources: []string{"tvdb", "tmdb"}, ProviderIDs: map[string]string{"tvdb": "2"}},
	}
	if _, ok := selectInitialMatchCandidate(hints, cands, nil); ok {
		t.Fatalf("two distinct shows must not be auto-accepted by the lone-result rule")
	}
}

func TestSelectInitialMatchCandidate_ConflictingProviderIDsNotAccepted(t *testing.T) {
	// Same title+year but different tmdb IDs => two distinct shows; must NOT auto-accept.
	// Each scores 65 (45 exact title + 20 year) + 5 + 1(richness) = ... actually
	// 45+20+5+1 = 71 (no sources, one provider ID), well above the 55 floor, so the new
	// branch is reached. candidatesAreSingleDistinctShow must return false because the two
	// candidates carry the same canonical provider key (tmdb) with conflicting values.
	hints := &MatchHints{Title: "Alpha", Year: 2022, Type: "movie"}
	cands := []MatchCandidate{
		{Title: "Alpha", Year: 2022, ContentType: "movie", ProviderIDs: map[string]string{"tmdb": "111"}},
		{Title: "Alpha", Year: 2022, ContentType: "movie", ProviderIDs: map[string]string{"tmdb": "222"}},
	}
	if _, ok := selectInitialMatchCandidate(hints, cands, nil); ok {
		t.Fatal("conflicting tmdb IDs must not be auto-accepted")
	}
}

func TestSelectInitialMatchCandidate_CrossSourceNoHintYearAcceptedByMultiSource(t *testing.T) {
	// Hint has NO year (0). Both providers return the same show (year 1999), tied.
	// Multi-source agreement substitutes for the missing hint year.
	hints := &MatchHints{Title: "100 Deeds for Eddie McDowd", Year: 0, Type: "series"}
	cands := []MatchCandidate{
		{Title: "100 Deeds for Eddie McDowd", Year: 1999, ContentType: "series", Sources: []string{"tvdb"}, ProviderIDs: map[string]string{"tvdb": "72450"}},
		{Title: "100 Deeds for Eddie McDowd", Year: 1999, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "6518"}},
	}
	got, ok := selectInitialMatchCandidate(hints, cands, []string{"tvdb", "tmdb"})
	if !ok || got == nil || got.ProviderIDs["tvdb"] != "72450" {
		t.Fatalf("expected tvdb winner via multi-source corroboration, got ok=%v cand=%+v", ok, got)
	}
}

func TestSelectInitialMatchCandidate_CrossSourceNoCandidateYearNotAccepted(t *testing.T) {
	// Both providers return the same title but neither carries a release year.
	// Year-equality between two 0-years is meaningless, so
	// candidatesAreSingleDistinctShow must reject and the multi-source
	// corroboration arm must NOT fire — otherwise two no-year cross-source
	// results would auto-accept, over-accepting ambiguous matches.
	hints := &MatchHints{Title: "Untitled Show", Year: 0, Type: "series"}
	cands := []MatchCandidate{
		{Title: "Untitled Show", Year: 0, ContentType: "series", Sources: []string{"tvdb"}, ProviderIDs: map[string]string{"tvdb": "111"}},
		{Title: "Untitled Show", Year: 0, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "222"}},
	}
	if got, ok := selectInitialMatchCandidate(hints, cands, []string{"tvdb", "tmdb"}); ok {
		t.Fatalf("no-year cross-source candidates must not be auto-accepted, got %+v", got)
	}
}

func TestSelectInitialMatchCandidate_LoneNoYearSingleSourceNotAccepted(t *testing.T) {
	// Single candidate, no hint year, single source: no year corroboration AND only
	// one source -> must NOT auto-accept (falls to the single-candidate >=70 gate).
	hints := &MatchHints{Title: "Some Obscure Show", Year: 0, Type: "series"}
	cands := []MatchCandidate{
		{Title: "Some Obscure Show", Year: 1999, ContentType: "series", Sources: []string{"tvdb"}, ProviderIDs: map[string]string{"tvdb": "999"}},
	}
	if got, ok := selectInitialMatchCandidate(hints, cands, []string{"tvdb"}); ok {
		t.Fatalf("lone no-year single-source result must not be auto-accepted, got %+v", got)
	}
}

func TestSelectInitialMatchCandidate_CrossSourceTieResolvedByProviderPriority(t *testing.T) {
	// "100 Days Wild" (2020, series, no shared IDs): TVDB and TMDB each return the correct
	// show (score 83 each: 45 exact title + 20 year + 12 source + 5 has IDs + 1 richness).
	// Two noise candidates (unrelated title/year) score 18 — well outside the 15-pt tie
	// window of 83, so topTieGroup contains only the two correct candidates.
	// candidatesAreSingleDistinctShow passes (same title/year, no conflicting IDs),
	// and pickByProviderPriority selects the winner by the library's chain order.
	hints := &MatchHints{Title: "100 Days Wild", Year: 2020, Type: "series"}
	cands := []MatchCandidate{
		{Title: "100 Days Wild", Year: 2020, ContentType: "series", Sources: []string{"tvdb"}, ProviderIDs: map[string]string{"tvdb": "386908"}},
		{Title: "100 Days Wild", Year: 2020, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "109476"}},
		{Title: "Some Other Show", Year: 2026, ContentType: "series", Sources: []string{"tvdb"}, ProviderIDs: map[string]string{"tvdb": "476741"}},
		{Title: "Live to 100", Year: 2023, ContentType: "series", Sources: []string{"tvdb"}, ProviderIDs: map[string]string{"tvdb": "437829"}},
	}

	// tvdb ranked first in provider chain -> tvdb candidate wins
	got, ok := selectInitialMatchCandidate(hints, cands, []string{"tvdb", "tmdb"})
	if !ok || got == nil || got.ProviderIDs["tvdb"] != "386908" {
		t.Fatalf("expected tvdb winner (386908), got ok=%v cand=%+v", ok, got)
	}

	// tmdb ranked first in provider chain -> tmdb candidate wins
	got, ok = selectInitialMatchCandidate(hints, cands, []string{"tmdb", "tvdb"})
	if !ok || got == nil || got.ProviderIDs["tmdb"] != "109476" {
		t.Fatalf("expected tmdb winner (109476), got ok=%v cand=%+v", ok, got)
	}

	// nil priority -> still accepts (fallback to top-scored, i.e. first in sorted order)
	got, ok = selectInitialMatchCandidate(hints, cands, nil)
	if !ok || got == nil {
		t.Fatalf("expected nil-priority to still accept a match, got ok=%v cand=%+v", ok, got)
	}
}

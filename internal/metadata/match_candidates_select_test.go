package metadata

import "testing"

func TestSelectInitialMatchCandidate_LoneResultYearMatchBelow70(t *testing.T) {
	// Exact title, matching year, NO sources, NO provider IDs => score 45+20 = 65 (<70).
	// Old behavior rejected this (single candidate <70); the new rule accepts it.
	hints := &MatchHints{Title: "1201", Year: 1993, Type: "movie"}
	cands := []MatchCandidate{{Title: "1201", Year: 1993, ContentType: "movie"}}
	got, ok := selectInitialMatchCandidate(hints, cands)
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
	got, ok := selectInitialMatchCandidate(hints, cands)
	if !ok || got == nil {
		t.Fatalf("expected same-show-across-sources to be accepted, got ok=%v cand=%+v", ok, got)
	}
}

func TestSelectInitialMatchCandidate_LoneResultYearMismatchStillRejected(t *testing.T) {
	// Exact title but year mismatch, one source => score 45+12 = 57 (in [55,70), no year bonus).
	// Year does NOT corroborate, so the new rule must NOT fire; single candidate <70 => reject.
	hints := &MatchHints{Title: "1201", Year: 1993, Type: "movie"}
	cands := []MatchCandidate{{Title: "1201", Year: 1990, ContentType: "movie", Sources: []string{"tmdb"}}}
	if got, ok := selectInitialMatchCandidate(hints, cands); ok {
		t.Fatalf("expected year-mismatch lone result to be rejected, got cand=%+v", got)
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
	if _, ok := selectInitialMatchCandidate(hints, cands); ok {
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
	if _, ok := selectInitialMatchCandidate(hints, cands); ok {
		t.Fatal("conflicting tmdb IDs must not be auto-accepted")
	}
}

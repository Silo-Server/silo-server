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
	// Two DIFFERENT shows (different titles, similar scores => gap=0 => tie-break path).
	// candidatesAreSingleDistinctShow returns false (titles differ), so the new rule
	// must NOT fire; falls through to the existing tie-break which returns nil because
	// DetailScore is 0. Guards against over-accepting distinct results.
	//
	// "The Show Special" and "The Show Extra" both score 38 against hint "The Show" /
	// 2010 (0-similarity title match, 1 source, 1 provider ID), gap = 0 < 15 =>
	// tie-break; inferTitleSimilarity between the two candidates is 0 => not same show.
	hints := &MatchHints{Title: "The Show", Year: 2010, Type: "movie"}
	cands := []MatchCandidate{
		{Title: "The Show Special", Year: 2010, ContentType: "movie", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "1"}},
		{Title: "The Show Extra", Year: 2010, ContentType: "movie", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "2"}},
	}
	if _, ok := selectInitialMatchCandidate(hints, cands); ok {
		t.Fatalf("two distinct shows must not be auto-accepted by the lone-result rule")
	}
}

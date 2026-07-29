package metadata

import (
	"slices"
	"testing"
)

const (
	adventureTimeTestTitle = "Adventure Time"
	exampleSeriesTestTitle = "Example Series"
	localizedEnglishTitle  = "Jubei-chan: The Ninja Girl"
	localizedOriginalTitle = "十兵衛ちゃん"
	localizedTVDBID        = "tvdb-series-1"
	localizedTMDBID        = "tmdb-series-1"
	testAlternateAliasKind = "alternate"
	testNFOProvider        = "nfo"
)

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
func TestSelectInitialMatchCandidate_LocalizedPrimaryTitleMatchesEnglishAlias(t *testing.T) {
	hints := &MatchHints{Title: localizedEnglishTitle, Year: 1999, Type: anchoredItemTypeSeries}
	candidates := []MatchCandidate{
		{
			Title:         localizedOriginalTitle,
			OriginalTitle: localizedOriginalTitle,
			TitleAliases: []TitleAlias{
				{Title: localizedEnglishTitle, Language: "en", Kind: testAlternateAliasKind, Provider: testTVDBProvider},
			},
			Year:        1999,
			ContentType: anchoredItemTypeSeries,
			Sources:     []string{testTVDBProvider},
			ProviderIDs: map[string]string{testTVDBProvider: localizedTVDBID, testTMDBProvider: localizedTMDBID},
		},
	}

	got, ok := selectInitialMatchCandidate(hints, candidates, nil)
	if !ok || got == nil {
		t.Fatal("localized primary title with an exact English alias was not matched")
	}
	if got.MatchedTitle != localizedEnglishTitle {
		t.Fatalf("MatchedTitle = %q, want English alias", got.MatchedTitle)
	}
}

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
func TestSelectInitialMatchCandidate_MissingYearTMDBTVDBTitleConsensus(t *testing.T) {
	hints := &MatchHints{Title: adventureTimeTestTitle, Type: anchoredItemTypeSeries}
	consensus := MatchCandidate{
		Title:       adventureTimeTestTitle,
		Year:        2010,
		ContentType: anchoredItemTypeSeries,
		Sources:     []string{testTVDBProvider, testTMDBProvider},
		ProviderIDs: map[string]string{testTMDBProvider: "15260", testTVDBProvider: "152831", testIMDBProvider: "tt1305826"},
	}
	competitor := MatchCandidate{
		Title:       adventureTimeTestTitle,
		Year:        1967,
		ContentType: "series",
		Sources:     []string{"tmdb"},
		ProviderIDs: map[string]string{"tmdb": "245745"},
	}

	for _, candidates := range [][]MatchCandidate{{consensus, competitor}, {competitor, consensus}} {
		got, ok := selectInitialMatchCandidate(hints, candidates, nil)
		if !ok || got == nil || got.ProviderIDs["tvdb"] != "152831" {
			t.Fatalf("ordered candidates %+v returned ok=%v candidate=%+v", candidates, ok, got)
		}
		if !slices.Contains(got.MatchReasons, "tmdb_tvdb_title_consensus") {
			t.Fatalf("MatchReasons = %v, want tmdb_tvdb_title_consensus", got.MatchReasons)
		}
	}
}

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
func TestSelectInitialMatchCandidate_MissingYearConsensusStripsCandidateYearDecoration(t *testing.T) {
	hints := &MatchHints{Title: adventureTimeTestTitle, Type: anchoredItemTypeSeries}
	candidates := []MatchCandidate{
		{
			Title:       adventureTimeTestTitle + " (2010)",
			Year:        2010,
			ContentType: anchoredItemTypeSeries,
			Sources:     []string{testTMDBProvider, testTVDBProvider},
			ProviderIDs: map[string]string{testTMDBProvider: "15260", testTVDBProvider: "152831"},
		},
		{
			Title:       adventureTimeTestTitle,
			Year:        1967,
			ContentType: anchoredItemTypeSeries,
			Sources:     []string{testTMDBProvider},
			ProviderIDs: map[string]string{testTMDBProvider: "245745"},
		},
	}

	got, ok := selectInitialMatchCandidate(hints, candidates, nil)
	if !ok || got == nil || got.ProviderIDs[testTVDBProvider] != "152831" {
		t.Fatalf("decorated corroborated candidate lost to competitor: ok=%v candidate=%+v", ok, got)
	}
	if !slices.Contains(got.MatchReasons, "tmdb_tvdb_title_consensus") {
		t.Fatalf("MatchReasons = %v, want tmdb_tvdb_title_consensus", got.MatchReasons)
	}
}

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
func TestSelectInitialMatchCandidate_MissingYearConsensusIsOrderIndependentAtEqualScore(t *testing.T) {
	hints := &MatchHints{Title: adventureTimeTestTitle, Type: anchoredItemTypeSeries}
	consensus := MatchCandidate{
		Title:       adventureTimeTestTitle,
		Year:        2010,
		ContentType: anchoredItemTypeSeries,
		Sources:     []string{testTMDBProvider, testTVDBProvider},
		ProviderIDs: map[string]string{testTMDBProvider: "15260", testTVDBProvider: "152831"},
	}
	equalScoreCompetitor := MatchCandidate{
		Title:       adventureTimeTestTitle,
		Year:        1967,
		ContentType: anchoredItemTypeSeries,
		Sources:     []string{testTMDBProvider, testTVDBProvider},
		ProviderIDs: map[string]string{testTMDBProvider: "245745", testIMDBProvider: "tt0000001"},
	}
	if consensusScore, competitorScore := scoreMatchCandidate(hints, consensus), scoreMatchCandidate(hints, equalScoreCompetitor); consensusScore != competitorScore {
		t.Fatalf("fixture scores differ: consensus=%v competitor=%v", consensusScore, competitorScore)
	}

	for _, candidates := range [][]MatchCandidate{{consensus, equalScoreCompetitor}, {equalScoreCompetitor, consensus}} {
		got, ok := selectInitialMatchCandidate(hints, candidates, nil)
		if !ok || got == nil || got.ProviderIDs[testTVDBProvider] != "152831" {
			t.Fatalf("ordered candidates %+v returned ok=%v candidate=%+v", candidates, ok, got)
		}
	}
}

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
func TestSelectInitialMatchCandidate_MissingYearConsensusBeatsSameYearSingleProvider(t *testing.T) {
	hints := &MatchHints{Title: exampleSeriesTestTitle, Type: "series"}
	candidates := []MatchCandidate{
		{
			Title:       exampleSeriesTestTitle,
			Year:        2021,
			ContentType: "series",
			Sources:     []string{"tmdb", "tvdb"},
			ProviderIDs: map[string]string{"tmdb": "101", "tvdb": "201"},
		},
		{
			Title:       exampleSeriesTestTitle,
			Year:        2021,
			ContentType: "series",
			Sources:     []string{"tmdb"},
			ProviderIDs: map[string]string{"tmdb": "102"},
		},
	}

	got, ok := selectInitialMatchCandidate(hints, candidates, nil)
	if !ok || got == nil || got.ProviderIDs["tvdb"] != "201" {
		t.Fatalf("same-year single-provider competitor displaced consensus: ok=%v candidate=%+v", ok, got)
	}
}

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
func TestSelectInitialMatchCandidate_MissingYearConsensusAllowsResolvedCanonicalIDConflict(t *testing.T) {
	hints := &MatchHints{Title: exampleSeriesTestTitle, Type: anchoredItemTypeSeries}
	candidates := NormalizeCandidatesForLanguage([]SearchResult{
		{
			Name:     exampleSeriesTestTitle,
			Year:     2021,
			Provider: testTVDBProvider,
			ProviderIDs: map[string]string{
				testIMDBProvider: "tt0000101",
				testTMDBProvider: "101",
				testTVDBProvider: "201",
			},
		},
		{
			Name:     exampleSeriesTestTitle,
			Year:     2021,
			Provider: testTMDBProvider,
			ProviderIDs: map[string]string{
				testIMDBProvider: "tt0000101",
				testTMDBProvider: "101",
				testTVDBProvider: "999",
			},
		},
		{
			Name:        exampleSeriesTestTitle,
			Year:        1991,
			Provider:    testTMDBProvider,
			ProviderIDs: map[string]string{testTMDBProvider: "102"},
		},
	}, anchoredItemTypeSeries, "en")
	if len(candidates) != 2 {
		t.Fatalf("NormalizeCandidatesForLanguage() returned %d candidates, want 2", len(candidates))
	}
	if candidates[0].ProviderIDs[testTVDBProvider] != "" || candidates[0].ConfirmedProviderIDs[testTVDBProvider] != "201" {
		t.Fatalf("resolved TVDB identity = provider:%q confirmed:%q", candidates[0].ProviderIDs[testTVDBProvider], candidates[0].ConfirmedProviderIDs[testTVDBProvider])
	}

	got, ok := selectInitialMatchCandidate(hints, candidates, nil)
	if !ok || got == nil || got.ConfirmedProviderIDs[testTVDBProvider] != "201" {
		t.Fatalf("resolved canonical ID conflict was not retained: ok=%v candidate=%+v", ok, got)
	}
}

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
func TestSelectInitialMatchCandidate_MissingYearConsensusNegativeControls(t *testing.T) {
	baseConsensus := MatchCandidate{
		Title:       adventureTimeTestTitle,
		Year:        2010,
		ContentType: "series",
		Sources:     []string{"tmdb", "tvdb"},
		ProviderIDs: map[string]string{"tmdb": "15260", "tvdb": "152831"},
	}
	baseCompetitor := MatchCandidate{
		Title:       adventureTimeTestTitle,
		Year:        1967,
		ContentType: "series",
		Sources:     []string{"tmdb"},
		ProviderIDs: map[string]string{"tmdb": "245745"},
	}

	tests := []struct {
		name       string
		hints      *MatchHints
		candidates []MatchCandidate
	}{
		{
			name:  "movie content type",
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: anchoredItemTypeMovie},
			candidates: []MatchCandidate{
				{Title: adventureTimeTestTitle, Year: 2010, ContentType: anchoredItemTypeMovie, Sources: []string{testTMDBProvider, testTVDBProvider}, ProviderIDs: map[string]string{testTMDBProvider: "1", testTVDBProvider: "2"}},
				{Title: adventureTimeTestTitle, Year: 1967, ContentType: anchoredItemTypeMovie, Sources: []string{testTMDBProvider}, ProviderIDs: map[string]string{testTMDBProvider: "3"}},
			},
		},
		{
			name:  "one remote provider even with cross references",
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: "series"},
			candidates: []MatchCandidate{
				{Title: adventureTimeTestTitle, Year: 2010, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "1", "tvdb": "2"}},
				baseCompetitor,
			},
		},
		{
			name:  "two corroborated identities",
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: "series"},
			candidates: []MatchCandidate{
				baseConsensus,
				{Title: adventureTimeTestTitle, Year: 1967, ContentType: "series", Sources: []string{"tmdb", "tvdb"}, ProviderIDs: map[string]string{"tmdb": "3", "tvdb": "4"}},
			},
		},
		{
			name:  "coherent title is not exact",
			hints: &MatchHints{Title: "The Real History of the World War", Type: "series"},
			candidates: []MatchCandidate{
				{Title: "The Real History of the World War Europe", Year: 2010, ContentType: "series", Sources: []string{"tmdb", "tvdb"}, ProviderIDs: map[string]string{"tmdb": "1", "tvdb": "2"}},
				{Title: "The Real History of the World War Pacific", Year: 2010, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "3"}},
			},
		},
		{
			name:  "missing retained tvdb id",
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: "series"},
			candidates: []MatchCandidate{
				{Title: adventureTimeTestTitle, Year: 2010, ContentType: "series", Sources: []string{"tmdb", "tvdb"}, ProviderIDs: map[string]string{"tmdb": "1"}},
				baseCompetitor,
			},
		},
		{
			name:  "quarantined tvdb id",
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: "series"},
			candidates: []MatchCandidate{
				{Title: adventureTimeTestTitle, Year: 2010, ContentType: "series", Sources: []string{"tmdb", "tvdb"}, ProviderIDs: map[string]string{"tmdb": "1", "tvdb": "2"}, ConflictingProviderIDKeys: []string{"tvdb"}},
				baseCompetitor,
			},
		},
		{
			name:  "nfo is not a second provider",
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: "series"},
			candidates: []MatchCandidate{
				{Title: adventureTimeTestTitle, Year: 2010, ContentType: anchoredItemTypeSeries, Sources: []string{testTMDBProvider, testNFOProvider}, ProviderIDs: map[string]string{testTMDBProvider: "1", testTVDBProvider: "2"}},
				baseCompetitor,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := selectInitialMatchCandidate(tt.hints, tt.candidates, nil); ok {
				t.Fatalf("negative control matched candidate %+v", got)
			}
		})
	}
}

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
func TestTMDBTVDBTitleConsensusWinner_NegativeControls(t *testing.T) {
	consensus := MatchCandidate{Title: adventureTimeTestTitle, Year: 2010, ContentType: "series", Sources: []string{"tmdb", "tvdb"}, ProviderIDs: map[string]string{"tmdb": "2", "tvdb": "3"}}
	competitor := MatchCandidate{Title: adventureTimeTestTitle, Year: 1967, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "1"}}
	tests := []struct {
		name   string
		hints  *MatchHints
		scored []scoredMatchCandidate
	}{
		{
			name:  "hint year present",
			hints: &MatchHints{Title: adventureTimeTestTitle, Year: 1967, Type: "series"},
			scored: []scoredMatchCandidate{
				{candidate: consensus, score: 80},
				{candidate: competitor, score: 80},
			},
		},
		{
			name:  "corroborated candidate is lower ranked",
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: "series"},
			scored: []scoredMatchCandidate{
				{candidate: competitor, score: 80},
				{candidate: consensus, score: 76},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := tmdbTVDBTitleConsensusWinner(tt.hints, tt.scored); ok {
				t.Fatalf("negative control returned candidate %+v", got)
			}
		})
	}
}

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

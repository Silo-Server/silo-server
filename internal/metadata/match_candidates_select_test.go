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

// matchCand builds a MatchCandidate for selection tables.
func matchCand(title string, year int, contentType string, ids map[string]string, sources ...string) MatchCandidate {
	return MatchCandidate{Title: title, Year: year, ContentType: contentType, ProviderIDs: ids, Sources: sources}
}

type selectInitialMatchCandidateDecisionsCase struct {
	name             string
	hints            *MatchHints
	candidates       []MatchCandidate
	priority         []string
	reject           bool
	wantIDs          map[string]string
	wantTitle        string
	wantMatchedTitle string
	wantReason       string
}

//nolint:goconst // Keep the production-shaped provider fixtures readable in place.
var selectInitialMatchCandidateDecisionsCases = func() []selectInitialMatchCandidateDecisionsCase {
	ids := func(kv ...string) map[string]string {
		out := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	movie := func(title string, year int) *MatchHints {
		return &MatchHints{Title: title, Year: year, Type: "movie"}
	}
	series := func(title string, year int) *MatchHints {
		return &MatchHints{Title: title, Year: year, Type: "series"}
	}
	const ufcTitle = "UFC 4: Revenge of the Warriors"
	ufc := func(name string, year, detail int, ids map[string]string, sources ...string) MatchCandidate {
		c := matchCand(name, year, "movie", ids, sources...)
		c.DetailScore = detail
		return c
	}
	adventureConsensus := matchCand(adventureTimeTestTitle, 2010, anchoredItemTypeSeries,
		ids(testTMDBProvider, "15260", testTVDBProvider, "152831", testIMDBProvider, "tt1305826"), testTVDBProvider, testTMDBProvider)
	baseConsensus := matchCand(adventureTimeTestTitle, 2010, "series", ids("tmdb", "15260", "tvdb", "152831"), "tmdb", "tvdb")
	baseCompetitor := matchCand(adventureTimeTestTitle, 1967, "series", ids("tmdb", "245745"), "tmdb")
	daysWild := []MatchCandidate{
		matchCand("100 Days Wild", 2020, "series", ids("tvdb", "386908"), "tvdb"),
		matchCand("100 Days Wild", 2020, "series", ids("tmdb", "109476"), "tmdb"),
		matchCand("Some Other Show", 2026, "series", ids("tvdb", "476741"), "tvdb"),
		matchCand("Live to 100", 2023, "series", ids("tvdb", "437829"), "tvdb"),
	}

	return []selectInitialMatchCandidateDecisionsCase{
		{
			name:       "local content id does not force trusted-ID matching",
			hints:      &MatchHints{ContentID: "local-skeleton-id", Title: "AEW Worlds End", Year: 2023, Type: "movie"},
			candidates: []MatchCandidate{matchCand("AEW Worlds End", 2023, "movie", ids("tmdb", "1217341"), "tmdb")},
		},
		{
			name:  "trusted id can win below noisy top result",
			hints: &MatchHints{Title: "10 Tricks", Year: 2022, Type: "movie", ImdbID: "tt0473100"},
			candidates: []MatchCandidate{
				matchCand("10 Tricks", 2022, "movie", ids("imdb", "tt9999999", "tmdb", "1"), "tmdb", "tvdb"),
				matchCand("Ten Tricks", 2021, "movie", ids("imdb", "tt0473100", "tmdb", "2"), "tmdb"),
			},
			wantIDs: ids("imdb", "tt0473100"),
		},
		{
			// Sole distinct candidate, exact title, year off by 2: scores below the
			// single-candidate >=70 gate but matches through title corroboration.
			name:       "sole exact title year off by two matches",
			hints:      movie("Stasi FC", 2023),
			candidates: []MatchCandidate{matchCand("Stasi FC", 2025, "movie", ids("tmdb", "111"), "tmdb")},
			wantIDs:    ids("tmdb", "111"),
		},
		{
			// A 3-year gap exceeds the ±2 bound and stays subject to the >=70 gate.
			name:       "sole exact title year off by three rejected",
			hints:      movie("Stasi FC", 2023),
			candidates: []MatchCandidate{matchCand("Stasi FC", 2026, "movie", ids("tmdb", "111"), "tmdb")},
			reject:     true,
		},
		{
			// Source agreement may replace a missing local year; it must not
			// override a known conflicting one.
			name:  "cross-provider agreement does not override year conflict",
			hints: series("The Piano Forest", 2007),
			candidates: []MatchCandidate{{
				Title:        "Five Fingers",
				TitleAliases: []TitleAlias{{Title: "Piano Forest", Language: "en", Kind: "alternate"}},
				Year:         2012,
				ContentType:  "series",
				ProviderIDs:  ids("imdb", "tt2242048", "tvdb", "261129"),
				Sources:      []string{"tmdb", "tvdb"},
			}},
			reject: true,
		},
		{
			// Below the 55 title-similarity floor: title corroboration must not
			// rescue a low-similarity title just because the year matches.
			name:       "sole different title exact year still floored",
			hints:      movie("Hotel Transylvania Puppy!", 2017),
			candidates: []MatchCandidate{matchCand("Puppy!", 2017, "movie", ids("tmdb", "222"), "tmdb")},
			reject:     true,
		},
		{name: "punctuation: colon variant", hints: movie("Anchorman The Legend of Ron Burgundy", 2004),
			candidates: []MatchCandidate{matchCand("Anchorman: The Legend of Ron Burgundy", 2004, "movie", ids("tmdb", "123"), "tmdb")},
			wantTitle:  "Anchorman: The Legend of Ron Burgundy"},
		{name: "punctuation: apostrophe and question mark variant", hints: movie("Whats Your Number", 2011),
			candidates: []MatchCandidate{matchCand("What's Your Number?", 2011, "movie", ids("tmdb", "123"), "tmdb")},
			wantTitle:  "What's Your Number?"},
		{name: "punctuation: ampersand variant", hints: movie("Tromeo and Juliet", 1996),
			candidates: []MatchCandidate{matchCand("Tromeo & Juliet", 1996, "movie", ids("tmdb", "123"), "tmdb")},
			wantTitle:  "Tromeo & Juliet"},
		{name: "punctuation: hyphen variant", hints: movie("Ant Man and the Wasp", 2018),
			candidates: []MatchCandidate{matchCand("Ant-Man and the Wasp", 2018, "movie", ids("tmdb", "123"), "tmdb")},
			wantTitle:  "Ant-Man and the Wasp"},
		{name: "punctuation: superscript digit variant", hints: movie("Alien 3", 1992),
			candidates: []MatchCandidate{matchCand("Alien³", 1992, "movie", ids("tmdb", "123"), "tmdb")},
			wantTitle:  "Alien³"},
		{name: "punctuation: comparison safe edition suffix variant", hints: movie("Zack Snyders Justice League Justice Is Gray", 2021),
			candidates: []MatchCandidate{matchCand("Zack Snyder's Justice League", 2021, "movie", ids("tmdb", "123"), "tmdb")},
			wantTitle:  "Zack Snyder's Justice League"},
		{
			name:  "provider title with repeated year",
			hints: movie("AEW Worlds End", 2023),
			candidates: []MatchCandidate{
				matchCand("AEW Worlds End 2023", 2023, "movie", ids("tmdb", "1217341"), "tmdb"),
				matchCand("AEW Worlds End 2023: Zero Hour", 2023, "movie", ids("tmdb", "1217342"), "tmdb"),
			},
			wantTitle: "AEW Worlds End 2023",
		},
		{
			name:  "detail score breaks duplicate provider tie",
			hints: movie("UFC 4 Revenge of the Warriors", 1994),
			candidates: []MatchCandidate{
				ufc(ufcTitle, 1994, 18, ids("tmdb", "1558410"), "tmdb"),
				ufc(ufcTitle, 1994, 46, ids("tmdb", "17508", "imdb", "tt0487980"), "tmdb"),
			},
			wantIDs: ids("tmdb", "17508"),
		},
		{name: "detail score: duplicate tie without clear detail gap", hints: movie("UFC 4 Revenge of the Warriors", 1994), reject: true,
			candidates: []MatchCandidate{
				ufc(ufcTitle, 1994, 28, ids("tmdb", "1558410"), "tmdb"),
				ufc(ufcTitle, 1994, 34, ids("tmdb", "17508"), "tmdb"),
			}},
		{name: "detail score: richer different-title candidate", hints: movie("UFC 4 Revenge of the Warriors", 1994), reject: true,
			candidates: []MatchCandidate{
				ufc(ufcTitle+" Event", 1994, 22, ids("tmdb", "17508"), "tmdb"),
				ufc("UFC 4 Revenge of the Warriors Bonus", 1994, 80, ids("tmdb", "999999", "imdb", "tt9999999"), "imdb", "tmdb"),
			}},
		{name: "detail score: duplicates without years", hints: movie("UFC 4 Revenge of the Warriors", 1994), reject: true,
			candidates: []MatchCandidate{
				ufc(ufcTitle, 0, 18, ids("tmdb", "1558410"), "tmdb"),
				ufc(ufcTitle, 0, 46, ids("tmdb", "17508", "imdb", "tt0487980"), "tmdb"),
			}},
		{name: "detail score: hint-incompatible type", hints: series("UFC 4 Revenge of the Warriors", 1994), reject: true,
			candidates: []MatchCandidate{
				ufc(ufcTitle, 1994, 18, ids("tmdb", "1558410"), "tmdb"),
				ufc(ufcTitle, 1994, 46, ids("tmdb", "17508", "imdb", "tt0487980"), "tmdb"),
			}},
		{
			name:  "provider order tie requires exact title and year",
			hints: series("100 Days Wild", 2020),
			candidates: []MatchCandidate{
				matchCand("100 Days Wild", 2020, "series", ids("tvdb", "383893"), "tvdb"),
				matchCand("Step Brothers", 2020, "series", ids("tmdb", "109792", "imdb", "tt1234567"), "imdb", "metadb", "tmdb", "xattr"),
			},
			reject: true,
		},
		{
			name:  "localized primary title matches English alias",
			hints: &MatchHints{Title: localizedEnglishTitle, Year: 1999, Type: anchoredItemTypeSeries},
			candidates: []MatchCandidate{{
				Title:         localizedOriginalTitle,
				OriginalTitle: localizedOriginalTitle,
				TitleAliases: []TitleAlias{
					{Title: localizedEnglishTitle, Language: "en", Kind: testAlternateAliasKind, Provider: testTVDBProvider},
				},
				Year:        1999,
				ContentType: anchoredItemTypeSeries,
				Sources:     []string{testTVDBProvider},
				ProviderIDs: ids(testTVDBProvider, localizedTVDBID, testTMDBProvider, localizedTMDBID),
			}},
			wantMatchedTitle: localizedEnglishTitle,
		},
		{
			name:       "missing year tmdb+tvdb title consensus, consensus first",
			hints:      &MatchHints{Title: adventureTimeTestTitle, Type: anchoredItemTypeSeries},
			candidates: []MatchCandidate{adventureConsensus, baseCompetitor},
			wantIDs:    ids("tvdb", "152831"),
			wantReason: "tmdb_tvdb_title_consensus",
		},
		{
			name:       "missing year tmdb+tvdb title consensus, competitor first",
			hints:      &MatchHints{Title: adventureTimeTestTitle, Type: anchoredItemTypeSeries},
			candidates: []MatchCandidate{baseCompetitor, adventureConsensus},
			wantIDs:    ids("tvdb", "152831"),
			wantReason: "tmdb_tvdb_title_consensus",
		},
		{
			name:  "missing year consensus strips candidate year decoration",
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: anchoredItemTypeSeries},
			candidates: []MatchCandidate{
				matchCand(adventureTimeTestTitle+" (2010)", 2010, anchoredItemTypeSeries, ids(testTMDBProvider, "15260", testTVDBProvider, "152831"), testTMDBProvider, testTVDBProvider),
				matchCand(adventureTimeTestTitle, 1967, anchoredItemTypeSeries, ids(testTMDBProvider, "245745"), testTMDBProvider),
			},
			wantIDs:    ids(testTVDBProvider, "152831"),
			wantReason: "tmdb_tvdb_title_consensus",
		},
		{name: "missing year consensus control: movie content type", reject: true,
			hints: &MatchHints{Title: adventureTimeTestTitle, Type: anchoredItemTypeMovie},
			candidates: []MatchCandidate{
				matchCand(adventureTimeTestTitle, 2010, anchoredItemTypeMovie, ids(testTMDBProvider, "1", testTVDBProvider, "2"), testTMDBProvider, testTVDBProvider),
				matchCand(adventureTimeTestTitle, 1967, anchoredItemTypeMovie, ids(testTMDBProvider, "3"), testTMDBProvider),
			}},
		{name: "missing year consensus control: one remote provider even with cross references", reject: true,
			hints: series(adventureTimeTestTitle, 0),
			candidates: []MatchCandidate{
				matchCand(adventureTimeTestTitle, 2010, "series", ids("tmdb", "1", "tvdb", "2"), "tmdb"),
				baseCompetitor,
			}},
		{name: "missing year consensus control: two corroborated identities", reject: true,
			hints: series(adventureTimeTestTitle, 0),
			candidates: []MatchCandidate{
				baseConsensus,
				matchCand(adventureTimeTestTitle, 1967, "series", ids("tmdb", "3", "tvdb", "4"), "tmdb", "tvdb"),
			}},
		{name: "missing year consensus control: coherent title is not exact", reject: true,
			hints: series("The Real History of the World War", 0),
			candidates: []MatchCandidate{
				matchCand("The Real History of the World War Europe", 2010, "series", ids("tmdb", "1", "tvdb", "2"), "tmdb", "tvdb"),
				matchCand("The Real History of the World War Pacific", 2010, "series", ids("tmdb", "3"), "tmdb"),
			}},
		{name: "missing year consensus control: missing retained tvdb id", reject: true,
			hints: series(adventureTimeTestTitle, 0),
			candidates: []MatchCandidate{
				matchCand(adventureTimeTestTitle, 2010, "series", ids("tmdb", "1"), "tmdb", "tvdb"),
				baseCompetitor,
			}},
		{name: "missing year consensus control: quarantined tvdb id", reject: true,
			hints: series(adventureTimeTestTitle, 0),
			candidates: []MatchCandidate{
				{Title: adventureTimeTestTitle, Year: 2010, ContentType: "series", Sources: []string{"tmdb", "tvdb"}, ProviderIDs: ids("tmdb", "1", "tvdb", "2"), ConflictingProviderIDKeys: []string{"tvdb"}},
				baseCompetitor,
			}},
		{name: "missing year consensus control: nfo is not a second provider", reject: true,
			hints: series(adventureTimeTestTitle, 0),
			candidates: []MatchCandidate{
				matchCand(adventureTimeTestTitle, 2010, anchoredItemTypeSeries, ids(testTMDBProvider, "1", testTVDBProvider, "2"), testTMDBProvider, testNFOProvider),
				baseCompetitor,
			}},
		{
			// Exact title, matching year, no sources, no provider IDs: 45+20 = 65 (<70).
			name:       "lone result year match below 70",
			hints:      movie("1201", 1993),
			candidates: []MatchCandidate{{Title: "1201", Year: 1993, ContentType: "movie"}},
			wantTitle:  "1201",
		},
		{
			// Exact title, year mismatch, one source: 45+12 = 57; year does not corroborate.
			name:       "lone result year mismatch still rejected",
			hints:      movie("1201", 1993),
			candidates: []MatchCandidate{{Title: "1201", Year: 1990, ContentType: "movie", Sources: []string{"tmdb"}}},
			reject:     true,
		},
		{
			// Multi-source evidence clears the 55 floor, but the title is not
			// coherent with the scanner hint.
			name:       "corroboration requires hint title coherence",
			hints:      movie("Hotel Transylvania Puppy!", 2017),
			candidates: []MatchCandidate{matchCand("Puppy!", 2017, "movie", ids("tmdb", "222"), "tmdb", "tvdb", "imdb")},
			priority:   []string{"tmdb", "tvdb", "imdb"},
			reject:     true,
		},
		{
			// Each candidate is coherent with the hint (Jaccard 7/8) and scores 78,
			// but they differ from each other (Jaccard 7/9), so
			// candidatesAreSingleDistinctShow is false and the tie-break returns nil.
			name:  "two different shows unchanged",
			hints: series("The Real History of the World War", 2010),
			candidates: []MatchCandidate{
				matchCand("The Real History of the World War Europe", 2010, "series", ids("tvdb", "1"), "tvdb", "tmdb"),
				matchCand("The Real History of the World War Pacific", 2010, "series", ids("tvdb", "2"), "tvdb", "tmdb"),
			},
			reject: true,
		},
		{
			// Same title and year but conflicting tmdb IDs: two distinct shows.
			name:  "conflicting provider ids not accepted",
			hints: movie("Alpha", 2022),
			candidates: []MatchCandidate{
				matchCand("Alpha", 2022, "movie", ids("tmdb", "111")),
				matchCand("Alpha", 2022, "movie", ids("tmdb", "222")),
			},
			reject: true,
		},
		{
			// Multi-source agreement substitutes for the missing hint year.
			name:  "cross source no hint year accepted by multi-source",
			hints: series("100 Deeds for Eddie McDowd", 0),
			candidates: []MatchCandidate{
				matchCand("100 Deeds for Eddie McDowd", 1999, "series", ids("tvdb", "72450"), "tvdb"),
				matchCand("100 Deeds for Eddie McDowd", 1999, "series", ids("tmdb", "6518"), "tmdb"),
			},
			priority: []string{"tvdb", "tmdb"},
			wantIDs:  ids("tvdb", "72450"),
		},
		{
			// Year equality between two 0-years is meaningless.
			name:  "cross source no candidate year not accepted",
			hints: series("Untitled Show", 0),
			candidates: []MatchCandidate{
				matchCand("Untitled Show", 0, "series", ids("tvdb", "111"), "tvdb"),
				matchCand("Untitled Show", 0, "series", ids("tmdb", "222"), "tmdb"),
			},
			priority: []string{"tvdb", "tmdb"},
			reject:   true,
		},
		{
			// No year corroboration and only one source: the >=70 gate applies.
			name:       "lone no-year single-source not accepted",
			hints:      series("Some Obscure Show", 0),
			candidates: []MatchCandidate{matchCand("Some Obscure Show", 1999, "series", ids("tvdb", "999"), "tvdb")},
			priority:   []string{"tvdb"},
			reject:     true,
		},
		{
			// An ID-less candidate (e.g. the NFO title-only search candidate) must
			// never win the proven-single-show tie-break on chain priority when an
			// ID-bearing member exists, and the winner adopts the union of the
			// group's provider IDs. nfo sits first in the chain priority.
			name:  "ID-less candidate excluded when an ID-bearing one exists",
			hints: movie("Inception", 2010),
			candidates: []MatchCandidate{
				matchCand("Inception", 2010, "movie", ids(), "nfo"),
				matchCand("Inception", 2010, "movie", ids("tmdb", "27205"), "tmdb"),
				matchCand("Inception", 2010, "movie", ids("tvdb", "83898"), "tvdb"),
			},
			priority: []string{"nfo", "tmdb", "tvdb"},
			wantIDs:  ids("tmdb", "27205", "tvdb", "83898"),
		},
		{
			// A local file echoing a title is not a second provider agreeing, so
			// it cannot stand in for a missing hint year.
			name:  "nfo source does not corroborate",
			hints: movie("Some Show", 0),
			candidates: []MatchCandidate{
				matchCand("Some Show", 2015, "movie", ids("tmdb", "1"), "tmdb"),
				matchCand("Some Show", 2015, "movie", ids(), "nfo"),
			},
			reject: true,
		},
		{
			name:  "remote multi-source group still corroborates",
			hints: movie("Some Show", 0),
			candidates: []MatchCandidate{
				matchCand("Some Show", 2015, "movie", ids("tmdb", "1"), "tmdb"),
				matchCand("Some Show", 2015, "movie", ids("tvdb", "2"), "tvdb"),
			},
		},
		// Two correct candidates score 83 each; the noise rows score 18, outside the
		// 15-point tie window. pickByProviderPriority follows the chain order.
		{name: "cross source tie resolved by provider priority: tvdb first", hints: series("100 Days Wild", 2020),
			candidates: daysWild, priority: []string{"tvdb", "tmdb"}, wantIDs: ids("tvdb", "386908")},
		{name: "cross source tie resolved by provider priority: tmdb first", hints: series("100 Days Wild", 2020),
			candidates: daysWild, priority: []string{"tmdb", "tvdb"}, wantIDs: ids("tmdb", "109476")},
		{name: "cross source tie resolved by provider priority: nil priority still accepts", hints: series("100 Days Wild", 2020),
			candidates: daysWild},
	}
}()

// TestSelectInitialMatchCandidate_Decisions holds the selectInitialMatchCandidate
// cases that differ only in hints, candidates and provider priority. A reject
// row must return no winner; an accept row must return a winner that carries
// every wanted provider ID, title, matched title and reason.
func TestSelectInitialMatchCandidate_Decisions(t *testing.T) {
	t.Parallel()

	for _, tt := range selectInitialMatchCandidateDecisionsCases {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := selectInitialMatchCandidate(tt.hints, tt.candidates, tt.priority)
			if tt.reject {
				if ok || got != nil {
					t.Fatalf("expected no match, got ok=%v winner=%+v", ok, got)
				}
				return
			}
			if !ok || got == nil {
				t.Fatalf("expected a match, got ok=%v winner=%+v", ok, got)
			}
			for key, want := range tt.wantIDs {
				if got.ProviderIDs[key] != want {
					t.Fatalf("winner %s id = %q, want %q (winner %+v)", key, got.ProviderIDs[key], want, got)
				}
			}
			if tt.wantTitle != "" && got.Title != tt.wantTitle {
				t.Fatalf("winner.Title = %q, want %q", got.Title, tt.wantTitle)
			}
			if tt.wantMatchedTitle != "" && got.MatchedTitle != tt.wantMatchedTitle {
				t.Fatalf("MatchedTitle = %q, want %q", got.MatchedTitle, tt.wantMatchedTitle)
			}
			if tt.wantReason != "" && !slices.Contains(got.MatchReasons, tt.wantReason) {
				t.Fatalf("MatchReasons = %v, want %s", got.MatchReasons, tt.wantReason)
			}
		})
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

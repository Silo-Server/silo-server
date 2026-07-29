//nolint:goconst // Repeated titles and provider keys keep scoring fixtures explicit.
package metadata

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSelectInitialMatchCandidate_IgnoresLocalContentIDForTrustedSelection(t *testing.T) {
	t.Parallel()

	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			ContentID: "local-skeleton-id",
			Title:     "AEW Worlds End",
			Year:      2023,
			Type:      "movie",
		},
		[]MatchCandidate{
			{
				Title:       "AEW Worlds End",
				Year:        2023,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1217341"},
				Sources:     []string{"tmdb"},
			},
		},
		nil,
	)
	if !ok || winner == nil {
		t.Fatal("expected local content_id not to force trusted-ID matching")
	}
}

func TestSelectInitialMatchCandidate_TrustedIDCanWinBelowNoisyTopResult(t *testing.T) {
	t.Parallel()

	winner, ok := selectInitialMatchCandidate(
		&MatchHints{Title: "10 Tricks", Year: 2022, Type: "movie", ImdbID: "tt0473100"},
		[]MatchCandidate{
			{
				Title:       "10 Tricks",
				Year:        2022,
				ContentType: "movie",
				ProviderIDs: map[string]string{"imdb": "tt9999999", "tmdb": "1"},
				Sources:     []string{"tmdb", "tvdb"},
			},
			{
				Title:       "Ten Tricks",
				Year:        2021,
				ContentType: "movie",
				ProviderIDs: map[string]string{"imdb": "tt0473100", "tmdb": "2"},
				Sources:     []string{"tmdb"},
			},
		},
		nil,
	)
	if !ok || winner == nil {
		t.Fatal("expected candidate carrying the trusted IMDb ID to win")
	}
	if got := winner.ProviderIDs["imdb"]; got != "tt0473100" {
		t.Fatalf("winner IMDb id = %q, want tt0473100", got)
	}
}

func TestBuildMatchDecisionClassifiesProviderFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		outcome string
	}{
		{name: "rate limit is transient", err: errors.New("provider returned HTTP 429"), outcome: "provider_transient"},
		{name: "bad request is permanent", err: errors.New("provider returned HTTP 400"), outcome: "provider_permanent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			decision := buildMatchDecision(&MatchHints{Title: "Example"}, nil, nil, false, []error{tt.err})
			if string(decision.Outcome) != tt.outcome {
				t.Fatalf("outcome = %q, want %q", decision.Outcome, tt.outcome)
			}
		})
	}
}

func TestBuildMatchDecisionOnlyReportsTrustedIDConflictForExplicitConflict(t *testing.T) {
	t.Parallel()
	hints := &MatchHints{Title: "10 Tricks", ImdbID: "tt0473100"}

	missingID := buildMatchDecision(hints, []MatchCandidate{{
		Title: "10 Tricks", ProviderIDs: map[string]string{"tmdb": "123"},
	}}, nil, false, nil)
	if missingID.Outcome != "candidate_rejected" {
		t.Fatalf("candidate with no IMDb id outcome = %q, want candidate_rejected", missingID.Outcome)
	}

	conflictingID := buildMatchDecision(hints, []MatchCandidate{{
		Title: "10 Tricks", ProviderIDs: map[string]string{"imdb": "tt9999999"},
	}}, nil, false, nil)
	if conflictingID.Outcome != "trusted_id_conflict" {
		t.Fatalf("conflicting IMDb id outcome = %q, want trusted_id_conflict", conflictingID.Outcome)
	}
}

func TestBuildMatchDecisionAttachesWinnerReasonsOnlyToIDLessWinner(t *testing.T) {
	t.Parallel()
	hints := &MatchHints{Title: "Shared", Year: 2020, Type: "series"}
	candidates := []MatchCandidate{
		{Title: "Shared", Year: 2020, ContentType: "series", Sources: []string{"first"}},
		{Title: "Shared", Year: 2020, ContentType: "series", Sources: []string{"second"}},
	}
	winner := candidates[1]
	winner.MatchReasons = append(winner.MatchReasons, "episode_title_corroboration:2_of_2")

	decision := buildMatchDecision(hints, candidates, &winner, true, nil)
	if len(decision.TopCandidates) != 2 {
		t.Fatalf("top candidates = %d, want 2", len(decision.TopCandidates))
	}
	for _, candidate := range decision.TopCandidates {
		hasWinnerReason := containsString(candidate.Reasons, "episode_title_corroboration:2_of_2")
		if candidate.Sources[0] == "second" && !hasWinnerReason {
			t.Fatalf("winner reasons = %v, want episode corroboration", candidate.Reasons)
		}
		if candidate.Sources[0] == "first" && hasWinnerReason {
			t.Fatalf("non-winner reasons = %v, must not contain winner-only evidence", candidate.Reasons)
		}
	}
}

func TestScoreMatchCandidate_DoesNotCountLocalNFOAsProviderCorroboration(t *testing.T) {
	t.Parallel()

	hints := &MatchHints{Title: "Shared", Type: "series"}
	remote := MatchCandidate{Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}}
	remoteAndNFO := remote
	remoteAndNFO.Sources = []string{"tmdb", "nfo"}

	remoteScore := scoreMatchCandidate(hints, remote)
	if got := scoreMatchCandidate(hints, remoteAndNFO); got != remoteScore {
		t.Fatalf("remote+nfo score = %.1f, want remote-only score %.1f", got, remoteScore)
	}
}

func TestMergePreferredTitleMetadataReplacesExplicitFallback(t *testing.T) {
	t.Parallel()
	accumulator := &MetadataResult{
		Title: "倒凶十将伝", TitleLanguage: "ja", TitleIsFallback: true,
	}
	mergePreferredTitleMetadata(accumulator, &MetadataResult{
		Title: "10 Tokyo Warriors", TitleLanguage: "en",
		OriginalTitle: "倒凶十将伝", OriginalLanguage: "ja",
	}, "en", "tmdb", true)
	if accumulator.Title != "10 Tokyo Warriors" || accumulator.TitleLanguage != "en" || accumulator.TitleIsFallback {
		t.Fatalf("localized title = (%q, %q, %v)", accumulator.Title, accumulator.TitleLanguage, accumulator.TitleIsFallback)
	}
}

func TestMergePreferredTitleMetadataPreservesUnclassifiedFirstProviderTitle(t *testing.T) {
	t.Parallel()
	accumulator := &MetadataResult{Title: "Sidecar Title"}
	mergePreferredTitleMetadata(accumulator, &MetadataResult{
		Title: "Localized Provider Title", TitleLanguage: "en",
	}, "en", "tmdb", true)
	if accumulator.Title != "Sidecar Title" {
		t.Fatalf("title = %q, want first-provider sidecar title", accumulator.Title)
	}
}

func TestMergePreferredTitleMetadataRequiresEveryProviderResponseToBeComplete(t *testing.T) {
	t.Parallel()
	accumulator := &MetadataResult{}
	mergePreferredTitleMetadata(accumulator, &MetadataResult{
		Title: "Complete Title", TitleLanguage: "en", TitleAliasesComplete: true,
	}, "en", "tmdb", true)
	mergePreferredTitleMetadata(accumulator, &MetadataResult{
		Title: "Legacy Title", TitleLanguage: "en", TitleAliasesComplete: false,
	}, "en", "tmdb", true)

	if complete := accumulator.titleAliasProviders["tmdb"]; complete {
		t.Fatal("mixed complete and partial responses granted alias deletion authority")
	}
}

func TestPreferredTitlesPreserveOldPluginPrimaryWithoutLanguageMetadata(t *testing.T) {
	t.Parallel()
	search := SearchResult{Name: "Localized Legacy Title", OriginalTitle: "Native Title"}
	title, language, fallback, rank := preferredSearchResultTitle(search, "en")
	if title != "Localized Legacy Title" || language != "" || fallback || rank != 3 {
		t.Fatalf("legacy search title = (%q, %q, %t, %d)", title, language, fallback, rank)
	}

	metadata := &MetadataResult{Title: "Localized Legacy Title", OriginalTitle: "Native Title"}
	title, language, fallback, rank = preferredMetadataResultTitle(metadata, "en")
	if title != "Localized Legacy Title" || language != "" || fallback || rank != 3 {
		t.Fatalf("legacy metadata title = (%q, %q, %t, %d)", title, language, fallback, rank)
	}
}

func TestSanitizeCandidateProviderIDsRequiresStrictPositiveIMDbID(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"tt1", "tt0000000", "tt12345678901", "nm1234567", "1234567"} {
		if got := sanitizeCandidateProviderIDs(map[string]string{"imdb": invalid}); len(got) != 0 {
			t.Fatalf("sanitize imdb %q = %#v, want empty", invalid, got)
		}
	}
	for _, valid := range []string{"tt0000001", "tt12345678", "tt123456789", "tt1234567890"} {
		if got := sanitizeCandidateProviderIDs(map[string]string{"imdb": valid}); got["imdb"] != valid {
			t.Fatalf("sanitize imdb %q = %#v", valid, got)
		}
	}
}

func TestSelectInitialMatchCandidate_SoleExactTitleYearOffByTwoMatches(t *testing.T) {
	t.Parallel()

	// Sole distinct candidate, exact title, year off by 2 (e.g. "Stasi FC (2023)"
	// vs TMDB's 2025). Scores in the 55-69 band — below the single-candidate >=70
	// gate — but the exact title on a lone result should now match via title
	// corroboration without lowering any threshold.
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{Title: "Stasi FC", Year: 2023, Type: "movie"},
		[]MatchCandidate{
			{
				Title:       "Stasi FC",
				Year:        2025,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "111"},
				Sources:     []string{"tmdb"},
			},
		},
		nil,
	)
	if !ok || winner == nil || winner.ProviderIDs["tmdb"] != "111" {
		t.Fatalf("expected sole exact-title year-off-by-2 candidate to match, got ok=%v winner=%+v", ok, winner)
	}
}

func TestSelectInitialMatchCandidate_SoleExactTitleYearOffByThreeRejected(t *testing.T) {
	t.Parallel()

	// A 3-year gap exceeds the ±2 bound: a same-title film three years apart is
	// not corroborated and stays subject to the single-candidate >=70 gate.
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{Title: "Stasi FC", Year: 2023, Type: "movie"},
		[]MatchCandidate{
			{
				Title:       "Stasi FC",
				Year:        2026,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "111"},
				Sources:     []string{"tmdb"},
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatalf("expected year-off-by-3 sole candidate to be rejected, got ok=%v winner=%+v", ok, winner)
	}
}

func TestSelectInitialMatchCandidate_CrossProviderAgreementDoesNotOverrideYearConflict(t *testing.T) {
	t.Parallel()

	// Both providers know the same show by the local alias, but the five-year
	// conflict is evidence that this is a different work. Source agreement may
	// replace a missing local year; it must not override a known conflicting one.
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{Title: "The Piano Forest", Year: 2007, Type: "series"},
		[]MatchCandidate{
			{
				Title:        "Five Fingers",
				TitleAliases: []TitleAlias{{Title: "Piano Forest", Language: "en", Kind: "alternate"}},
				Year:         2012,
				ContentType:  "series",
				ProviderIDs:  map[string]string{"imdb": "tt2242048", "tvdb": "261129"},
				Sources:      []string{"tmdb", "tvdb"},
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatalf("expected cross-provider year conflict to be rejected, got ok=%v winner=%+v", ok, winner)
	}
}

func TestSelectInitialMatchCandidate_SoleDifferentTitleExactYearStillFloored(t *testing.T) {
	t.Parallel()

	// "Hotel Transylvania Puppy!" vs TMDB's "Puppy!" (same year) scores below the
	// 55 floor on title similarity, so it must stay rejected — title corroboration
	// must not rescue a low-similarity title just because the year matches.
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{Title: "Hotel Transylvania Puppy!", Year: 2017, Type: "movie"},
		[]MatchCandidate{
			{
				Title:       "Puppy!",
				Year:        2017,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "222"},
				Sources:     []string{"tmdb"},
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatalf("expected low-similarity sole candidate to stay rejected, got ok=%v winner=%+v", ok, winner)
	}
}

func TestSuppressTitleYearFallbackForTrustedIDs_IgnoresMetadb(t *testing.T) {
	t.Parallel()

	query := suppressTitleYearFallbackForTrustedIDs(SearchQuery{
		Title:       "AEW Worlds End",
		Year:        2023,
		ContentType: "movie",
		ProviderIDs: map[string]string{"metadb": "local-skeleton-id"},
	})

	if query.Title != "AEW Worlds End" || query.Year != 2023 {
		t.Fatalf("title/year were suppressed for metadb: title=%q year=%d", query.Title, query.Year)
	}
}

func TestNormalizeCandidates(t *testing.T) {
	tests := []struct {
		name    string
		results []SearchResult
		content string
		wantLen int
		check   func(t *testing.T, candidates []MatchCandidate)
	}{
		{
			name: "merge two providers with identical provider ID fingerprint",
			results: []SearchResult{
				{
					Name:        "The Matrix",
					Year:        1999,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "603"},
					ImageURL:    "https://tmdb.org/matrix.jpg",
					Overview:    "A computer hacker learns about the true nature of reality.",
				},
				{
					Name:        "The Matrix",
					Year:        1999,
					Provider:    "metadb",
					ProviderIDs: map[string]string{"tmdb": "603"},
					Overview:    "Neo discovers the Matrix.",
				},
			},
			content: "movie",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				c := candidates[0]
				if c.Title != "The Matrix" {
					t.Errorf("Title = %q, want %q", c.Title, "The Matrix")
				}
				if c.ProviderIDs["tmdb"] != "603" {
					t.Errorf("ProviderIDs[tmdb] = %q, want %q", c.ProviderIDs["tmdb"], "603")
				}
				if len(c.Sources) != 2 {
					t.Fatalf("Sources len = %d, want 2", len(c.Sources))
				}
				// Sources are sorted alphabetically.
				if c.Sources[0] != "metadb" || c.Sources[1] != "tmdb" {
					t.Errorf("Sources = %v, want [metadb tmdb]", c.Sources)
				}
			},
		},
		{
			name: "merge compatible candidates with overlapping provider IDs",
			results: []SearchResult{
				{
					Name:        "The Rookie: Feds",
					Year:        2022,
					Provider:    "tvdb",
					ProviderIDs: map[string]string{"tvdb": "420105", "imdb": "tt18076310"},
				},
				{
					Name:        "The Rookie: Feds",
					Year:        2022,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "201992", "tvdb": "420105", "imdb": "tt18076310"},
				},
			},
			content: "series",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				c := candidates[0]
				if c.ProviderIDs["tmdb"] != "201992" {
					t.Fatalf("tmdb id = %q, want 201992", c.ProviderIDs["tmdb"])
				}
				if c.ProviderIDs["tvdb"] != "420105" || c.ProviderIDs["imdb"] != "tt18076310" {
					t.Fatalf("provider ids = %+v, want tvdb and imdb preserved", c.ProviderIDs)
				}
				if len(c.Sources) != 2 {
					t.Fatalf("sources = %+v, want two providers", c.Sources)
				}
			},
		},
		{
			name: "do not merge candidates with conflicting overlapping provider IDs",
			results: []SearchResult{
				{
					Name:        "Show A",
					Year:        2022,
					Provider:    "tvdb",
					ProviderIDs: map[string]string{"tvdb": "420105", "imdb": "tt18076310"},
				},
				{
					Name:        "Show B",
					Year:        2022,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "201992", "tvdb": "999999", "imdb": "tt18076310"},
				},
			},
			content: "series",
			wantLen: 2,
			check: func(t *testing.T, candidates []MatchCandidate) {
				if len(candidates) != 2 {
					t.Fatalf("len(candidates) = %d, want 2", len(candidates))
				}
			},
		},
		{
			name: "merge two-ID consensus and quarantine conflicting third ID",
			results: []SearchResult{
				{
					Name:     "A Teacher",
					Year:     2020,
					Provider: "tvdb",
					ProviderIDs: map[string]string{
						"imdb": "tt10680614", "tmdb": "103992", "tvdb": "352440",
					},
				},
				{
					Name:     "A Teacher",
					Year:     2020,
					Provider: "tmdb",
					ProviderIDs: map[string]string{
						"imdb": "tt10680614", "tmdb": "103992", "tvdb": "473725",
					},
				},
			},
			content: "series",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				candidate := candidates[0]
				if candidate.ProviderIDs["imdb"] != "tt10680614" || candidate.ProviderIDs["tmdb"] != "103992" {
					t.Fatalf("agreed provider IDs = %+v", candidate.ProviderIDs)
				}
				if candidate.ProviderIDs["tvdb"] != "" {
					t.Fatalf("conflicting TVDB ID was retained: %+v", candidate.ProviderIDs)
				}
				if len(candidate.ConflictingProviderIDKeys) != 1 || candidate.ConflictingProviderIDKeys[0] != "tvdb" {
					t.Fatalf("quarantined keys = %v, want [tvdb]", candidate.ConflictingProviderIDKeys)
				}
				if candidate.ConfirmedProviderIDs["tvdb"] != "352440" {
					t.Fatalf("native TVDB resolution = %q, want 352440", candidate.ConfirmedProviderIDs["tvdb"])
				}
				annotateCandidateMatch(&candidate, &MatchHints{Title: "A Teacher", Type: "series"})
				if !containsString(candidate.MatchReasons, "provider_id_consensus") ||
					!containsString(candidate.MatchReasons, "resolved_tvdb_id") {
					t.Fatalf("match reasons = %v", candidate.MatchReasons)
				}
			},
		},
		{
			name: "discard malformed canonical provider cross references",
			results: []SearchResult{
				{
					Name:     "Fast & Furious: Spy Racers",
					Year:     2019,
					Provider: "tvdb",
					ProviderIDs: map[string]string{
						"imdb": "TT8322592", "tmdb": "95594-fast-furious-spy-racers", "tvdb": "362429",
					},
				},
			},
			content: "series",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				ids := candidates[0].ProviderIDs
				if ids["tmdb"] != "" || ids["tvdb"] != "362429" || ids["imdb"] != "tt8322592" {
					t.Fatalf("sanitized provider IDs = %+v", ids)
				}
			},
		},
		{
			name: "no recognized provider IDs gets synthetic key and stays separate",
			results: []SearchResult{
				{
					Name:        "Obscure Film",
					Year:        2020,
					Provider:    "custom-provider",
					ProviderIDs: map[string]string{"custom": "abc123"},
				},
				{
					Name:        "Another Film",
					Year:        2021,
					Provider:    "custom-provider",
					ProviderIDs: map[string]string{"custom": "def456"},
				},
			},
			content: "movie",
			wantLen: 2,
			check: func(t *testing.T, candidates []MatchCandidate) {
				if candidates[0].Title != "Obscure Film" {
					t.Errorf("candidates[0].Title = %q, want %q", candidates[0].Title, "Obscure Film")
				}
				if candidates[1].Title != "Another Film" {
					t.Errorf("candidates[1].Title = %q, want %q", candidates[1].Title, "Another Film")
				}
				// Each should have exactly one source.
				for i, c := range candidates {
					if len(c.Sources) != 1 {
						t.Errorf("candidates[%d].Sources len = %d, want 1", i, len(c.Sources))
					}
				}
			},
		},
		{
			name: "agreement hints computed when 2+ sources agree",
			results: []SearchResult{
				{
					Name:        "Inception",
					Year:        2010,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "27205"},
				},
				{
					Name:        "Inception",
					Year:        2010,
					Provider:    "tvdb",
					ProviderIDs: map[string]string{"tmdb": "27205"},
				},
			},
			content: "movie",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				c := candidates[0]
				if len(c.AgreementHints) != 1 {
					t.Fatalf("AgreementHints len = %d, want 1", len(c.AgreementHints))
				}
				want := "agreed_by_tmdb_and_tvdb"
				if c.AgreementHints[0] != want {
					t.Errorf("AgreementHints[0] = %q, want %q", c.AgreementHints[0], want)
				}
			},
		},
		{
			name: "no agreement hint for single source",
			results: []SearchResult{
				{
					Name:        "Solo",
					Year:        2023,
					Provider:    "tmdb",
					ProviderIDs: map[string]string{"tmdb": "99999"},
				},
			},
			content: "movie",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				if len(candidates[0].AgreementHints) != 0 {
					t.Errorf("AgreementHints = %v, want empty", candidates[0].AgreementHints)
				}
			},
		},
		{
			name: "ImageURL and Overview fallback to first non-empty",
			results: []SearchResult{
				{
					Name:        "Dune",
					Year:        2021,
					Provider:    "provider-a",
					ProviderIDs: map[string]string{"tmdb": "438631"},
					ImageURL:    "",
					Overview:    "",
				},
				{
					Name:        "Dune",
					Year:        2021,
					Provider:    "provider-b",
					ProviderIDs: map[string]string{"tmdb": "438631"},
					ImageURL:    "https://example.com/dune.jpg",
					Overview:    "A noble family becomes embroiled in a war.",
				},
				{
					Name:        "Dune",
					Year:        2021,
					Provider:    "provider-c",
					ProviderIDs: map[string]string{"tmdb": "438631"},
					ImageURL:    "https://other.com/dune2.jpg",
					Overview:    "Should not win; provider-b was first.",
				},
			},
			content: "movie",
			wantLen: 1,
			check: func(t *testing.T, candidates []MatchCandidate) {
				c := candidates[0]
				if c.ImageURL != "https://example.com/dune.jpg" {
					t.Errorf("ImageURL = %q, want first non-empty from provider-b", c.ImageURL)
				}
				if c.Overview != "A noble family becomes embroiled in a war." {
					t.Errorf("Overview = %q, want first non-empty from provider-b", c.Overview)
				}
			},
		},
		{
			name: "insertion order stability",
			results: []SearchResult{
				{
					Name:        "First",
					Year:        2001,
					Provider:    "p1",
					ProviderIDs: map[string]string{"tmdb": "1"},
				},
				{
					Name:        "Second",
					Year:        2002,
					Provider:    "p2",
					ProviderIDs: map[string]string{"tmdb": "2"},
				},
				{
					Name:        "Third",
					Year:        2003,
					Provider:    "p3",
					ProviderIDs: map[string]string{"tmdb": "3"},
				},
			},
			content: "movie",
			wantLen: 3,
			check: func(t *testing.T, candidates []MatchCandidate) {
				titles := []string{"First", "Second", "Third"}
				for i, want := range titles {
					if candidates[i].Title != want {
						t.Errorf("candidates[%d].Title = %q, want %q", i, candidates[i].Title, want)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeCandidates(tt.results, tt.content)
			if len(got) != tt.wantLen {
				t.Fatalf("len(candidates) = %d, want %d", len(got), tt.wantLen)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestNormalizeCandidatesForLanguage_PrefersKnownLibraryAlias(t *testing.T) {
	results := []SearchResult{
		{
			Name: "倒凶十将伝", OriginalTitle: "倒凶十将伝", OriginalLanguage: "ja",
			TitleLanguage: "ja", TitleIsFallback: true,
			TitleAliases: []TitleAlias{{Title: "10 Tokyo Warriors", Language: "en", Kind: "alternate"}},
			Year:         1999, Provider: "tvdb", ProviderIDs: map[string]string{"tvdb": "123"},
		},
		{
			Name: "10 Tokyo Warriors", OriginalTitle: "倒凶十将伝", OriginalLanguage: "ja",
			TitleLanguage: "en",
			Year:          1999, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "456", "tvdb": "123"},
		},
	}

	candidates := NormalizeCandidatesForLanguage(results, "series", "en")
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1: %+v", len(candidates), candidates)
	}
	if candidates[0].Title != "10 Tokyo Warriors" || candidates[0].OriginalTitle != "倒凶十将伝" {
		t.Fatalf("localized candidate = %+v", candidates[0])
	}

	winner, ok := selectInitialMatchCandidate(&MatchHints{Title: "10 Tokyo Warriors", Type: "series"}, candidates, []string{"tvdb", "tmdb"})
	if !ok || winner == nil {
		t.Fatal("expected alias-coherent multi-source candidate to be selected")
	}
	if winner.MatchedTitle != "10 Tokyo Warriors" || winner.MatchScore < 70 {
		t.Fatalf("winner diagnostics = %+v", winner)
	}
}

func TestNormalizeCandidates_UnknownLanguageAliasMatchesButDoesNotBecomePrimary(t *testing.T) {
	candidates := NormalizeCandidatesForLanguage([]SearchResult{{
		Name: "倒凶十将伝", OriginalTitle: "倒凶十将伝", OriginalLanguage: "ja",
		TitleLanguage: "ja", TitleIsFallback: true,
		TitleAliases: []TitleAlias{{Title: "10 Tokyo Warriors", Kind: "alternate"}},
		Year:         1999, Provider: "tvdb", ProviderIDs: map[string]string{"tvdb": "123"},
	}}, "series", "en")
	if got := candidates[0].Title; got != "倒凶十将伝" {
		t.Fatalf("primary title = %q, want native fallback", got)
	}
	annotateCandidateMatch(&candidates[0], &MatchHints{Title: "10 Tokyo Warriors", Type: "series"})
	if got := candidates[0].MatchedTitle; got != "10 Tokyo Warriors" {
		t.Fatalf("matched title = %q", got)
	}
}

func TestNormalizeCandidatesForLanguage_PrefersNativeTitleOverNonNativeFallback(t *testing.T) {
	candidates := NormalizeCandidatesForLanguage([]SearchResult{{
		Name: "Titre de secours", OriginalTitle: "Native Title", OriginalLanguage: "ja",
		TitleLanguage: "fr", TitleIsFallback: true,
		Provider: "tvdb", ProviderIDs: map[string]string{"tvdb": "123"},
	}}, "series", "en")
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	if got := candidates[0].Title; got != "Native Title" {
		t.Fatalf("primary title = %q, want provider-confirmed native title", got)
	}
	if got := candidates[0].TitleLanguage; got != "ja" {
		t.Fatalf("title language = %q, want ja", got)
	}
}

func TestPreferredMetadataResultTitle_PrefersNativeTitleOverNonNativeFallback(t *testing.T) {
	title, language, fallback, rank := preferredMetadataResultTitle(&MetadataResult{
		Title: "Titre de secours", OriginalTitle: "Native Title", OriginalLanguage: "jpn",
		TitleLanguage: "fr", TitleIsFallback: true,
	}, "en")
	if title != "Native Title" || language != "ja" || !fallback || rank != 1 {
		t.Fatalf("preferred title = %q, %q, %t, %d", title, language, fallback, rank)
	}
}

func TestMergePreferredTitleMetadataClassifiesNativeFallbackAsOriginalAlias(t *testing.T) {
	accumulator := &MetadataResult{}
	mergePreferredTitleMetadata(accumulator, &MetadataResult{
		Title: "倒凶十将伝", OriginalTitle: "倒凶十将伝", OriginalLanguage: "ja",
		TitleLanguage: "ja", TitleIsFallback: true,
	}, "en", "tvdb", true)
	if len(accumulator.TitleAliases) != 1 || accumulator.TitleAliases[0].Kind != "original" || accumulator.TitleAliases[0].Language != "ja" {
		t.Fatalf("native aliases = %#v", accumulator.TitleAliases)
	}
}

func TestMergePreferredTitleMetadataDoesNotAttributeIdentityHintAliases(t *testing.T) {
	t.Parallel()
	accumulator := &MetadataResult{}
	mergePreferredTitleMetadata(accumulator, &MetadataResult{
		Title:                "Curated Sidecar Title",
		TitleLanguage:        "en",
		OriginalTitle:        "Native Sidecar Title",
		OriginalLanguage:     "ja",
		TitleAliases:         []TitleAlias{{Title: "Sidecar Alternate", Language: "en", Kind: "alternate"}},
		TitleAliasesComplete: true,
	}, "en", "nfo", false)

	if accumulator.Title != "Curated Sidecar Title" || accumulator.OriginalTitle != "Native Sidecar Title" {
		t.Fatalf("metadata fields were not retained: %#v", accumulator)
	}
	if len(accumulator.TitleAliases) != 0 {
		t.Fatalf("identity-hint aliases = %#v, want none", accumulator.TitleAliases)
	}
	if _, attributed := accumulator.titleAliasProviders["nfo"]; attributed {
		t.Fatal("identity-hint provider received alias persistence authority")
	}
}

func TestSelectInitialMatchCandidateDirectIMDbIDFor10Tricks(t *testing.T) {
	t.Parallel()
	candidates := NormalizeCandidatesForLanguage([]SearchResult{{
		Name: "Ten Tricks", Year: 2022, Provider: "tmdb",
		ProviderIDs: map[string]string{"imdb": "tt0473100", "tmdb": "12345"},
	}}, "movie", "en")
	winner, ok := selectInitialMatchCandidate(&MatchHints{
		Title: "10 Tricks", Year: 2022, Type: "movie", ImdbID: "tt0473100",
	}, candidates, []string{"tmdb"})
	if !ok || winner == nil {
		t.Fatal("trusted IMDb ID did not produce a decisive match")
	}
	if winner.ProviderIDs["imdb"] != "tt0473100" || winner.MatchScore < 100 {
		t.Fatalf("winner = %#v", winner)
	}
}

func TestNormalizeTitleForScoring_NumberWordsAndOrdinals(t *testing.T) {
	for _, pair := range [][2]string{
		{"10 Tricks", "Ten Tricks"},
		{"Dune Part Two", "Dune Part 2"},
		{"The 10th Kingdom", "The Tenth Kingdom"},
	} {
		if got, want := normalizeTitleForScoring(pair[0]), normalizeTitleForScoring(pair[1]); got != want {
			t.Errorf("normalize %q = %q, %q = %q", pair[0], got, pair[1], want)
		}
	}
}

func TestSelectInitialMatchCandidate_AcceptsSinglePunctuationEquivalentCandidate(t *testing.T) {
	tests := []struct {
		name           string
		hintTitle      string
		candidateTitle string
		year           int
	}{
		{
			name:           "colon variant",
			hintTitle:      "Anchorman The Legend of Ron Burgundy",
			candidateTitle: "Anchorman: The Legend of Ron Burgundy",
			year:           2004,
		},
		{
			name:           "apostrophe and question mark variant",
			hintTitle:      "Whats Your Number",
			candidateTitle: "What's Your Number?",
			year:           2011,
		},
		{
			name:           "ampersand variant",
			hintTitle:      "Tromeo and Juliet",
			candidateTitle: "Tromeo & Juliet",
			year:           1996,
		},
		{
			name:           "hyphen variant",
			hintTitle:      "Ant Man and the Wasp",
			candidateTitle: "Ant-Man and the Wasp",
			year:           2018,
		},
		{
			name:           "superscript digit variant",
			hintTitle:      "Alien 3",
			candidateTitle: "Alien³",
			year:           1992,
		},
		{
			name:           "comparison safe edition suffix variant",
			hintTitle:      "Zack Snyders Justice League Justice Is Gray",
			candidateTitle: "Zack Snyder's Justice League",
			year:           2021,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			winner, ok := selectInitialMatchCandidate(
				&MatchHints{
					Title: tt.hintTitle,
					Year:  tt.year,
					Type:  "movie",
				},
				[]MatchCandidate{
					{
						Title:       tt.candidateTitle,
						Year:        tt.year,
						ContentType: "movie",
						ProviderIDs: map[string]string{"tmdb": "123"},
						Sources:     []string{"tmdb"},
					},
				},
				nil,
			)
			if !ok || winner == nil {
				t.Fatalf("expected lone punctuation-equivalent candidate to be accepted")
			}
			if winner.Title != tt.candidateTitle {
				t.Fatalf("winner.Title = %q, want %q", winner.Title, tt.candidateTitle)
			}
		})
	}
}

func TestSelectInitialMatchCandidate_AcceptsProviderTitleWithRepeatedYear(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "AEW Worlds End",
			Year:  2023,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "AEW Worlds End 2023",
				Year:        2023,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1217341"},
				Sources:     []string{"tmdb"},
			},
			{
				Title:       "AEW Worlds End 2023: Zero Hour",
				Year:        2023,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1217342"},
				Sources:     []string{"tmdb"},
			},
		},
		nil,
	)
	if !ok || winner == nil {
		t.Fatal("expected provider title with repeated release year to be accepted")
	}
	if winner.Title != "AEW Worlds End 2023" {
		t.Fatalf("winner.Title = %q, want AEW Worlds End 2023", winner.Title)
	}
}

func TestSelectInitialMatchCandidate_UsesDetailScoreForDuplicateProviderTie(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "UFC 4 Revenge of the Warriors",
			Year:  1994,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1558410"},
				Sources:     []string{"tmdb"},
				DetailScore: 18,
			},
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "17508", "imdb": "tt0487980"},
				Sources:     []string{"tmdb"},
				DetailScore: 46,
			},
		},
		nil,
	)
	if !ok || winner == nil {
		t.Fatal("expected richer duplicate TMDB candidate to be accepted")
	}
	if got := winner.ProviderIDs["tmdb"]; got != "17508" {
		t.Fatalf("winner tmdb = %q, want 17508", got)
	}
}

func TestSelectInitialMatchCandidate_RejectsDuplicateTieWithoutClearDetailGap(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "UFC 4 Revenge of the Warriors",
			Year:  1994,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1558410"},
				Sources:     []string{"tmdb"},
				DetailScore: 28,
			},
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "17508"},
				Sources:     []string{"tmdb"},
				DetailScore: 34,
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatal("expected duplicate tie without clear detail gap to remain unmatched")
	}
}

func TestSelectInitialMatchCandidate_UsesProviderOrderForExactCrossProviderTie(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "100 Days Wild",
			Year:  2020,
			Type:  "series",
		},
		[]MatchCandidate{
			{
				Title:       "100 Days Wild",
				Year:        2020,
				ContentType: "series",
				ProviderIDs: map[string]string{"tvdb": "383893"},
				Sources:     []string{"tvdb"},
			},
			{
				Title:       "100 Days Wild",
				Year:        2020,
				ContentType: "series",
				ProviderIDs: map[string]string{"tmdb": "109792"},
				Sources:     []string{"tmdb"},
			},
		},
		nil,
	)
	if !ok || winner == nil {
		t.Fatal("expected exact cross-provider tie to use provider order")
	}
	if got := winner.ProviderIDs["tvdb"]; got != "383893" {
		t.Fatalf("winner tvdb = %q, want 383893", got)
	}
}

func TestSelectInitialMatchCandidate_ProviderOrderTieRequiresExactTitleYear(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "100 Days Wild",
			Year:  2020,
			Type:  "series",
		},
		[]MatchCandidate{
			{
				Title:       "100 Days Wild",
				Year:        2020,
				ContentType: "series",
				ProviderIDs: map[string]string{"tvdb": "383893"},
				Sources:     []string{"tvdb"},
			},
			{
				Title:       "Step Brothers",
				Year:        2020,
				ContentType: "series",
				ProviderIDs: map[string]string{"tmdb": "109792", "imdb": "tt1234567"},
				Sources:     []string{"imdb", "metadb", "tmdb", "xattr"},
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatal("expected non-equivalent cross-provider tie to remain unmatched")
	}
}

func TestSelectInitialMatchCandidate_DetailScoreDoesNotOverrideDifferentTitleTie(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "UFC 4 Revenge of the Warriors",
			Year:  1994,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "UFC 4: Revenge of the Warriors Event",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "17508"},
				Sources:     []string{"tmdb"},
				DetailScore: 22,
			},
			{
				Title:       "UFC 4 Revenge of the Warriors Bonus",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "999999", "imdb": "tt9999999"},
				Sources:     []string{"imdb", "tmdb"},
				DetailScore: 80,
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatal("expected richer different-title candidate to be rejected")
	}
}

func TestSelectInitialMatchCandidate_DetailScoreRequiresDatedDuplicateCandidates(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "UFC 4 Revenge of the Warriors",
			Year:  1994,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "UFC 4: Revenge of the Warriors",
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1558410"},
				Sources:     []string{"tmdb"},
				DetailScore: 18,
			},
			{
				Title:       "UFC 4: Revenge of the Warriors",
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "17508", "imdb": "tt0487980"},
				Sources:     []string{"tmdb"},
				DetailScore: 46,
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatal("expected duplicate detail tie-breaker to reject candidates without matching years")
	}
}

func TestSelectInitialMatchCandidate_DetailScoreRequiresHintCompatibleType(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "UFC 4 Revenge of the Warriors",
			Year:  1994,
			Type:  "series",
		},
		[]MatchCandidate{
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1558410"},
				Sources:     []string{"tmdb"},
				DetailScore: 18,
			},
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "17508", "imdb": "tt0487980"},
				Sources:     []string{"tmdb"},
				DetailScore: 46,
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatal("expected duplicate detail tie-breaker to reject candidates with hint-incompatible type")
	}
}

func TestSelectInitialMatchCandidate_RejectsWeakSingleCandidate(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "Anchorman The Legend of Ron Burgundy",
			Year:  2004,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "Step Brothers",
				Year:        2008,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "12133"},
				Sources:     []string{"tmdb"},
			},
		},
		nil,
	)
	if ok || winner != nil {
		t.Fatalf("expected weak lone candidate to be rejected")
	}
}

func TestSelectRefreshMatchCandidate_AcceptsCandidateWithPartialTrustedIDCoverage(t *testing.T) {
	winner, ok := selectRefreshMatchCandidate(
		&models.MediaItem{
			Title:  "The Matrix",
			Year:   1999,
			Type:   "movie",
			TmdbID: "603",
			ImdbID: "tt0133093",
		},
		nil,
		[]MatchCandidate{
			{
				Title:       "The Matrix",
				Year:        1999,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "603"},
				Sources:     []string{"tmdb"},
			},
		},
	)
	if !ok || winner == nil {
		t.Fatalf("expected partial trusted-ID coverage candidate to be accepted")
	}
}

func TestSelectRefreshMatchCandidate_RejectsCandidateWithoutTrustedIDMatches(t *testing.T) {
	winner, ok := selectRefreshMatchCandidate(
		&models.MediaItem{
			Title:  "The Matrix",
			Year:   1999,
			Type:   "movie",
			TmdbID: "603",
			ImdbID: "tt0133093",
		},
		nil,
		[]MatchCandidate{
			{
				Title:       "The Matrix",
				Year:        1999,
				ContentType: "movie",
				ProviderIDs: map[string]string{},
				Sources:     []string{"tmdb"},
			},
		},
	)
	if ok || winner != nil {
		t.Fatalf("expected candidate without trusted-ID matches to be rejected")
	}
}

func TestSelectRefreshMatchCandidate_RejectsConflictingTrustedIDCandidate(t *testing.T) {
	winner, ok := selectRefreshMatchCandidate(
		&models.MediaItem{
			Title:  "The Matrix",
			Year:   1999,
			Type:   "movie",
			TmdbID: "603",
			ImdbID: "tt0133093",
		},
		nil,
		[]MatchCandidate{
			{
				Title:       "The Matrix",
				Year:        1999,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "603", "imdb": "tt9999999"},
				Sources:     []string{"tmdb"},
			},
		},
	)
	if ok || winner != nil {
		t.Fatalf("expected conflicting trusted-ID candidate to be rejected")
	}
}

func TestApplyCandidateProviderIDConsensusKeepsNonConflictingAggregatorIDs(t *testing.T) {
	candidates := NormalizeCandidates([]SearchResult{{
		Name:     "Example Movie",
		Provider: "metadb",
		ProviderIDs: map[string]string{
			"tmdb": "999",
			"imdb": "tt7654321",
		},
	}}, "movie")
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}

	ids := map[string]string{}
	applyCandidateProviderIDConsensus(ids, &candidates[0], nil)
	if ids["tmdb"] != "999" || ids["imdb"] != "tt7654321" {
		t.Fatalf("aggregator provider ids = %#v, want tmdb/imdb retained", ids)
	}
}

func TestApplyCandidateProviderIDConsensusPromotesIDsConfirmedByBothProviders(t *testing.T) {
	candidates := NormalizeCandidates([]SearchResult{
		{
			Name: "Example", Provider: "tvdb",
			ProviderIDs: map[string]string{"tvdb": "405851", "tmdb": "1234", "imdb": "tt12236904"},
		},
		{
			Name: "Example", Provider: "tmdb",
			ProviderIDs: map[string]string{"tvdb": "405851", "tmdb": "1234", "imdb": "tt12236904"},
		},
	}, "series")
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}

	ids := map[string]string{}
	applyCandidateProviderIDConsensus(ids, &candidates[0], nil)
	if ids["tvdb"] != "405851" || ids["tmdb"] != "1234" || ids["imdb"] != "tt12236904" {
		t.Fatalf("confirmed provider ids = %#v, want tmdb/tvdb/imdb", ids)
	}
}

func TestApplyCandidateProviderIDConsensusPrefersOwningProviderDuringConflict(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "foreign cross-reference first"
		results := []SearchResult{
			{
				Name: "Under the Pole", Provider: "tvdb",
				ProviderIDs: map[string]string{"tvdb": "405851", "tmdb": "12236904", "imdb": "tt12236904"},
			},
			{
				Name: "Under the Pole", Provider: "tmdb",
				ProviderIDs: map[string]string{"tvdb": "405851", "tmdb": "987654", "imdb": "tt12236904"},
			},
		}
		if reverse {
			name = "owning provider first"
			results[0], results[1] = results[1], results[0]
		}
		t.Run(name, func(t *testing.T) {
			candidates := NormalizeCandidates(results, "series")
			if len(candidates) != 1 {
				t.Fatalf("candidate count = %d, want 1", len(candidates))
			}
			ids := map[string]string{}
			applyCandidateProviderIDConsensus(ids, &candidates[0], nil)
			if ids["tmdb"] != "987654" {
				t.Fatalf("resolved tmdb id = %q, want owning-provider value 987654", ids["tmdb"])
			}
			if ids["tvdb"] != "405851" || ids["imdb"] != "tt12236904" {
				t.Fatalf("resolved provider ids = %#v", ids)
			}
		})
	}
}

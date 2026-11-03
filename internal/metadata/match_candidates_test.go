//nolint:goconst // Repeated titles and provider keys keep scoring fixtures explicit.
package metadata

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

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

func TestSelectRefreshMatchCandidate_TrustedIDCoverage(t *testing.T) {
	item := &models.MediaItem{Title: "The Matrix", Year: 1999, Type: "movie", TmdbID: "603", ImdbID: "tt0133093"}
	tests := []struct {
		name        string
		providerIDs map[string]string
		wantMatch   bool
	}{
		{name: "accepts candidate with partial trusted-ID coverage", providerIDs: map[string]string{"tmdb": "603"}, wantMatch: true},
		{name: "rejects candidate without trusted-ID matches", providerIDs: map[string]string{}},
		{name: "rejects conflicting trusted-ID candidate", providerIDs: map[string]string{"tmdb": "603", "imdb": "tt9999999"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			winner, ok := selectRefreshMatchCandidate(item, nil, []MatchCandidate{{
				Title:       "The Matrix",
				Year:        1999,
				ContentType: "movie",
				ProviderIDs: tt.providerIDs,
				Sources:     []string{"tmdb"},
			}})
			if tt.wantMatch && (!ok || winner == nil) {
				t.Fatalf("expected candidate to be accepted")
			}
			if !tt.wantMatch && (ok || winner != nil) {
				t.Fatalf("expected candidate to be rejected, got %+v", winner)
			}
		})
	}
}

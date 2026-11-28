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

// The persisted decision names the rule that rejected the candidates, so an
// admin is not shown a passing score against the floor without the reason.
func TestBuildMatchDecisionNamesRejectingRule(t *testing.T) {
	t.Parallel()

	tmdbMovie := func(title string, year int, id string) MatchCandidate {
		return MatchCandidate{
			Title: title, Year: year, ContentType: "movie",
			ProviderIDs: map[string]string{"tmdb": id}, Sources: []string{"tmdb"},
		}
	}
	tests := []struct {
		name          string
		hints         *MatchHints
		candidates    []MatchCandidate
		rejection     MatchRejection
		requiredScore float64
	}{
		{
			name:          "lone undated candidate without a year",
			hints:         &MatchHints{Title: "4th Man Out", Type: "movie"},
			candidates:    []MatchCandidate{tmdbMovie("4th Man Out", 0, "342672")},
			rejection:     MatchRejectionLoneCandidate,
			requiredScore: automaticMatchLoneCandidateScore,
		},
		{
			name:          "below the floor",
			hints:         &MatchHints{Title: "Northern Lights", Type: "movie"},
			candidates:    []MatchCandidate{tmdbMovie("Quiet Harbor", 2015, "1")},
			rejection:     MatchRejectionBelowFloor,
			requiredScore: automaticMatchAcceptanceFloor,
		},
		{
			name:       "year more than two years off",
			hints:      &MatchHints{Title: "Example", Year: 2010, Type: "movie"},
			candidates: []MatchCandidate{tmdbMovie("Example", 2015, "1")},
			rejection:  MatchRejectionYearMismatch,
		},
		{
			name:       "top candidates tied",
			hints:      &MatchHints{Title: "Example", Type: "movie"},
			candidates: []MatchCandidate{tmdbMovie("Example", 2001, "1"), tmdbMovie("Example", 2015, "2")},
			rejection:  MatchRejectionAmbiguous,
		},
		{
			name:       "trusted id not returned",
			hints:      &MatchHints{Title: "Example", ImdbID: "tt0473100", Type: "movie"},
			candidates: []MatchCandidate{tmdbMovie("Example", 2015, "1")},
			rejection:  MatchRejectionTrustedIDMissing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			decision := boundedMatchDecision(buildMatchDecision(tt.hints, tt.candidates, nil, false, nil))
			if decision.Outcome != MatchOutcomeCandidateRejected {
				t.Fatalf("outcome = %q, want candidate_rejected", decision.Outcome)
			}
			if decision.Rejection != tt.rejection || decision.RequiredScore != tt.requiredScore {
				t.Fatalf("rejection = %q (required %v), want %q (required %v)",
					decision.Rejection, decision.RequiredScore, tt.rejection, tt.requiredScore)
			}
		})
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

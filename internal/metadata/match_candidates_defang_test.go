package metadata

import (
	"testing"
)

// The nfo source must not manufacture an "agreed_by" hint with a remote
// provider when their results merge into one candidate.
func TestNormalizeCandidates_NFOExcludedFromAgreementHints(t *testing.T) {
	results := []SearchResult{
		{Name: "Movie", Year: 2020, ProviderIDs: map[string]string{"tmdb": "7"}, Provider: "tmdb"},
		{Name: "Movie", Year: 2020, ProviderIDs: map[string]string{"tmdb": "7"}, Provider: "nfo"},
	}
	candidates := NormalizeCandidates(results, "movie")
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 merged candidate", len(candidates))
	}
	if len(candidates[0].AgreementHints) != 0 {
		t.Errorf("agreement hints = %v, want none (nfo does not corroborate)", candidates[0].AgreementHints)
	}

	// Two genuine remote sources still agree.
	remote := []SearchResult{
		{Name: "Movie", Year: 2020, ProviderIDs: map[string]string{"tmdb": "7"}, Provider: "tmdb"},
		{Name: "Movie", Year: 2020, ProviderIDs: map[string]string{"tmdb": "7"}, Provider: "trakt"},
	}
	candidates = NormalizeCandidates(remote, "movie")
	if len(candidates) != 1 || len(candidates[0].AgreementHints) != 1 {
		t.Fatalf("remote agreement hints = %+v, want one", candidates)
	}
}

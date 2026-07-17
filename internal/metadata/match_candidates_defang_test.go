package metadata

import (
	"testing"
)

// An ID-less candidate (e.g. the NFO title-only search candidate) must never
// win the proven-single-show tie-break on chain priority when an ID-bearing
// member exists in the group — and the winner must adopt the union of the
// group's provider IDs.
func TestSelectInitialMatch_IDlessCandidateExcludedWhenIDBearingExists(t *testing.T) {
	hints := &MatchHints{Title: "Inception", Year: 2010, Type: "movie"}
	candidates := []MatchCandidate{
		{Title: "Inception", Year: 2010, ContentType: "movie", ProviderIDs: map[string]string{}, Sources: []string{"nfo"}},
		{Title: "Inception", Year: 2010, ContentType: "movie", ProviderIDs: map[string]string{"tmdb": "27205"}, Sources: []string{"tmdb"}},
		{Title: "Inception", Year: 2010, ContentType: "movie", ProviderIDs: map[string]string{"tvdb": "83898"}, Sources: []string{"tvdb"}},
	}

	// nfo sits first in the library chain priority.
	winner, ok := selectInitialMatchCandidate(hints, candidates, []string{"nfo", "tmdb", "tvdb"})
	if !ok || winner == nil {
		t.Fatalf("winner = %v ok = %v, want accepted candidate", winner, ok)
	}
	if winner.ProviderIDs["tmdb"] != "27205" {
		t.Errorf("winner tmdb id = %q, want 27205 (ID-less candidate must not win; union adopted)", winner.ProviderIDs["tmdb"])
	}
	if winner.ProviderIDs["tvdb"] != "83898" {
		t.Errorf("winner tvdb id = %q, want 83898 (union of the proven-single-show group's IDs)", winner.ProviderIDs["tvdb"])
	}
}

// The nfo source must not count as independent corroboration: a local file
// echoing a title is not a second provider agreeing. With no hint year, a
// remote candidate corroborated only by the NFO must not be auto-accepted via
// the multi-source arm.
func TestSelectInitialMatch_NFOSourceDoesNotCorroborate(t *testing.T) {
	hints := &MatchHints{Title: "Some Show", Type: "movie"} // no year hint
	candidates := []MatchCandidate{
		{Title: "Some Show", Year: 2015, ContentType: "movie", ProviderIDs: map[string]string{"tmdb": "1"}, Sources: []string{"tmdb"}},
		{Title: "Some Show", Year: 2015, ContentType: "movie", ProviderIDs: map[string]string{}, Sources: []string{"nfo"}},
	}

	winner, ok := selectInitialMatchCandidate(hints, candidates, nil)
	if ok {
		t.Fatalf("winner = %+v, want rejection (nfo echo must not stand in for a missing year)", winner)
	}
}

// A genuinely multi-source group (two remote providers) still corroborates.
func TestSelectInitialMatch_RemoteMultiSourceStillCorroborates(t *testing.T) {
	hints := &MatchHints{Title: "Some Show", Type: "movie"} // no year hint
	candidates := []MatchCandidate{
		{Title: "Some Show", Year: 2015, ContentType: "movie", ProviderIDs: map[string]string{"tmdb": "1"}, Sources: []string{"tmdb"}},
		{Title: "Some Show", Year: 2015, ContentType: "movie", ProviderIDs: map[string]string{"tvdb": "2"}, Sources: []string{"tvdb"}},
	}

	winner, ok := selectInitialMatchCandidate(hints, candidates, nil)
	if !ok || winner == nil {
		t.Fatalf("winner = %v ok = %v, want accepted candidate", winner, ok)
	}
}

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

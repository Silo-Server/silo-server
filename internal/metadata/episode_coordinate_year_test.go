package metadata

import "testing"

// Coordinate-only promotion keeps the year guard the score-based pick
// applies: a folder named "The Office (2005)" holding S01E01 and S02E01 must
// not match the 2001 show of the same title because it has both episodes
// (#5373). Within the guard's two-year window the coordinates still decide.
func TestEpisodeCoordinateEvidenceKeepsTheYearGuard(t *testing.T) {
	t.Parallel()

	candidate := matchCand("The Office", 2001, "series", map[string]string{"tvdb": "78107", "tmdb": "2996"}, "tvdb", "tmdb")
	candidate.MatchReasons = []string{"exact_title"}
	local := []localEpisodeMatchHint{{SeasonNumber: 1, EpisodeNumber: 1}, {SeasonNumber: 2, EpisodeNumber: 1}}
	validation := episodeCandidateValidation{candidate: candidate, evaluated: true, coordinates: len(local)}

	for _, tc := range []struct {
		folderYear int
		want       bool
	}{
		{folderYear: 2005, want: false},
		{folderYear: 2003, want: true},
		{folderYear: 0, want: true},
	} {
		got := episodeCoordinateEvidenceIsSufficient(validation, episodeCandidateValidation{}, local, tc.folderYear, 1, true)
		if got != tc.want {
			t.Errorf("folder year %d: sufficient = %v, want %v", tc.folderYear, got, tc.want)
		}
	}
}

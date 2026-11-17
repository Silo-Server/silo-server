package filesplit

import "testing"

func TestDeriveEpisodePairs(t *testing.T) {
	t.Parallel()
	moved := []File{
		{ID: 1, EpisodeID: "episode-tvdb-100-1-1", SeasonNumber: 1, EpisodeNumber: 1},
		{ID: 2, EpisodeID: "episode-tvdb-100-1-2", SeasonNumber: 1, EpisodeNumber: 2},
		{ID: 3, EpisodeID: "episode-tvdb-100-1-2", SeasonNumber: 1, EpisodeNumber: 2}, // dup version
		{ID: 4, EpisodeID: "", SeasonNumber: 1, EpisodeNumber: 3},                     // never linked
		{ID: 5, EpisodeID: "episode-tvdb-100-0-0", SeasonNumber: 0, EpisodeNumber: 0}, // unparsed
	}

	pairs := DeriveEpisodePairs("series", moved, "series-tvdb-200")
	if len(pairs) != 2 {
		t.Fatalf("pairs = %v, want 2", pairs)
	}
	if pairs[0].To != "episode-tvdb-200-1-1" || pairs[1].To != "episode-tvdb-200-1-2" {
		t.Fatalf("pair targets = %v, want re-anchored deterministic ids", pairs)
	}

	// Local target: no deterministic anchor, no pairs.
	if got := DeriveEpisodePairs("series", moved, "local-abcdef"); got != nil {
		t.Fatalf("local target pairs = %v, want nil", got)
	}
	// Movies never pair.
	if got := DeriveEpisodePairs("movie", moved, "movie-tmdb-2"); got != nil {
		t.Fatalf("movie pairs = %v, want nil", got)
	}
}

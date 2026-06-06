package naming

import "testing"

func TestStripInferProviderTags_HandlesBrokenSonarrTokens(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"A Girl & Her Guard Dog [tvdb-{TvdbId}]", "A Girl & Her Guard Dog"},
		{"A Raven in the Harem [tvdb-{TvdbId}]", "A Raven in the Harem"},
		{"A Salad Bowl of Eccentrics {imdb-}", "A Salad Bowl of Eccentrics"},
		{"Coronation Street {imdb-}", "Coronation Street"},
		// well-formed tags must still be stripped:
		{"Some Show [tvdb-81189]", "Some Show"},
		{"Some Show {tmdb-27205}", "Some Show"},
		// no tag: unchanged:
		{"Cowboy Bebop", "Cowboy Bebop"},
	}
	for _, c := range cases {
		if got := stripInferProviderTags(c.in); got != c.want {
			t.Errorf("stripInferProviderTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

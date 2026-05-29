package sections

import "testing"

func TestCanonicalTrendingKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src, win         string
		wantSrc, wantWin string
	}{
		{"tmdb", "day", "tmdb", "day"},
		{"tmdb", "week", "tmdb", "week"},
		{"tmdb", "", "tmdb", "week"},
		{"", "day", "tmdb", "day"},
		{"", "", "tmdb", "week"},
		{"trakt", "day", "trakt", "week"},
		{"trakt", "week", "trakt", "week"},
		{"trakt", "", "trakt", "week"},
		{"bogus", "bogus", "tmdb", "week"},
	}
	for _, c := range cases {
		gotSrc, gotWin := canonicalTrendingKey(c.src, c.win)
		if gotSrc != c.wantSrc || gotWin != c.wantWin {
			t.Errorf("canonicalTrendingKey(%q, %q) = (%q, %q); want (%q, %q)",
				c.src, c.win, gotSrc, gotWin, c.wantSrc, c.wantWin)
		}
	}
}

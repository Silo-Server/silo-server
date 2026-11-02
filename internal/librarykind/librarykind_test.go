package librarykind

import "testing"

func TestPredicates(t *testing.T) {
	cases := []struct {
		name string
		fn   func(string) bool
		in   string
		want bool
	}{
		{"IsMovie", IsMovie, "movie", true},
		{"IsMovie", IsMovie, "movies", true},
		{"IsMovie", IsMovie, "  MOVIES  ", true},
		{"IsMovie", IsMovie, "mixed", false},

		{"IsTV", IsTV, "series", true},
		{"IsTV", IsTV, "tv", true},
		{"IsTV", IsTV, "show", true},
		{"IsTV", IsTV, "tvshows", true},
		{"IsTV", IsTV, "TV", true},
		{"IsTV", IsTV, "mixed", false},

		{"IsMixed", IsMixed, "mixed", true},
		{"IsMixed", IsMixed, " Mixed ", true},
		{"IsMixed", IsMixed, "movies", false},

		{"IsAudiobook", IsAudiobook, "audiobooks", true},
		{"IsAudiobook", IsAudiobook, "audiobook", true},
		{"IsAudiobook", IsAudiobook, "  AUDIOBOOKS  ", true},
		{"IsAudiobook", IsAudiobook, "movies", false},

		{"IsPodcast", IsPodcast, "podcasts", true},
		{"IsPodcast", IsPodcast, "podcast", true},
		{"IsPodcast", IsPodcast, "  PODCASTS  ", true},
		{"IsPodcast", IsPodcast, "series", false},

		{"IsEbook", IsEbook, "ebooks", true},
		{"IsEbook", IsEbook, "ebook", true},
		{"IsEbook", IsEbook, "  EBOOKS  ", true},
		{"IsEbook", IsEbook, "audiobooks", false},

		{"IsManga", IsManga, "manga", true},
		{"IsManga", IsManga, "  MANGA  ", true},
		{"IsManga", IsManga, "ebooks", false},
	}
	for _, tc := range cases {
		if got := tc.fn(tc.in); got != tc.want {
			t.Errorf("%s(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestOf(t *testing.T) {
	if got := Of(" Audiobooks "); got != (Kinds{Audiobook: true}) {
		t.Errorf("Of(audiobooks) = %+v", got)
	}
	if got := Of("mixed"); got != (Kinds{Mixed: true}) {
		t.Errorf("Of(mixed) = %+v", got)
	}
	if got := Of("movies"); got != (Kinds{Movie: true}) {
		t.Errorf("Of(movies) = %+v", got)
	}
	if got := Of("unknown"); got != (Kinds{}) {
		t.Errorf("Of(unknown) = %+v", got)
	}
}

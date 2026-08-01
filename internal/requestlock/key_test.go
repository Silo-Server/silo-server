package requestlock

import "testing"

func TestMediaKeyUsesStrongestAvailableIdentity(t *testing.T) {
	tests := []struct {
		name                 string
		mediaType            string
		tmdbID               int
		tvdbID, imdbID, want string
		wantOK               bool
	}{
		{name: "tmdb", mediaType: "movie", tmdbID: 603, tvdbID: "999", imdbID: "tt0133093", want: "silo:request-media:movie:tmdb:603", wantOK: true},
		{name: "tvdb fallback", mediaType: " series ", tvdbID: "0042", imdbID: "tt1", want: "silo:request-media:series:tvdb:42", wantOK: true},
		{name: "imdb fallback", mediaType: "MOVIE", imdbID: " TT0133093 ", want: "silo:request-media:movie:imdb:tt0133093", wantOK: true},
		{name: "no global zero key", mediaType: "movie", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := MediaKey(test.mediaType, test.tmdbID, test.tvdbID, test.imdbID)
			if got != test.want || ok != test.wantOK {
				t.Fatalf("MediaKey() = %q,%v; want %q,%v", got, ok, test.want, test.wantOK)
			}
		})
	}
}

package notifications

import (
	"testing"
)

func TestPublicArtworkURL(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", ""},
		{"tmdb poster scheme", "tmdb://poster/abc.jpg", "https://image.tmdb.org/t/p/w500/abc.jpg"},
		{"tmdb backdrop scheme", "tmdb://backdrop/bd.jpg", "https://image.tmdb.org/t/p/w500/bd.jpg"},
		{"tmdb malformed", "tmdb://abc.jpg", ""},
		{"tvdb scheme", "tvdb://banners/posters/x.jpg", "https://artworks.thetvdb.com/banners/posters/x.jpg"},
		{"verbatim https", "https://example-provider.com/p.jpg", "https://example-provider.com/p.jpg"},
		// Locally cached artwork must never resolve: a storage URL would name
		// the server's own origin in an outbound payload.
		{"cached storage key", "tmdb/movies/550/poster/original.jpg", ""},
		{"unknown plugin scheme", "metadb://poster/x.jpg", ""},
		{"legacy dash", "-", ""},
	}
	for _, tc := range cases {
		if got := publicArtworkURL(tc.path); got != tc.want {
			t.Errorf("%s: publicArtworkURL(%q) = %q, want %q", tc.name, tc.path, got, tc.want)
		}
	}
}

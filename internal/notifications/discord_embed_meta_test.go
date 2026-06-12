package notifications

import (
	"strings"
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

func TestProviderIDsURLs(t *testing.T) {
	movie := providerIDs{MediaType: "movie", IMDB: "tt1160419", TMDB: "438631", TVDB: "290"}
	if got := movie.tmdbURL(); got != "https://www.themoviedb.org/movie/438631" {
		t.Fatalf("unexpected movie tmdb URL %q", got)
	}
	if got := movie.tvdbURL(); got != "https://thetvdb.com/dereferrer/movie/290" {
		t.Fatalf("unexpected movie tvdb URL %q", got)
	}
	const severanceTMDBURL = "https://www.themoviedb.org/tv/95396"
	series := providerIDs{MediaType: "series", TMDB: "95396", TVDB: "371980"}
	if got := series.tmdbURL(); got != severanceTMDBURL {
		t.Fatalf("unexpected series tmdb URL %q", got)
	}
	if got := series.tvdbURL(); got != "https://thetvdb.com/dereferrer/series/371980" {
		t.Fatalf("unexpected series tvdb URL %q", got)
	}
	if got := series.titleURL(); got != series.tmdbURL() {
		t.Fatalf("title URL must prefer TMDB, got %q", got)
	}
	imdbOnly := providerIDs{IMDB: "tt1160419"}
	if got := imdbOnly.titleURL(); got != "https://www.imdb.com/title/tt1160419/" {
		t.Fatalf("title URL must fall back to IMDb, got %q", got)
	}
	if (providerIDs{}).titleURL() != "" || (providerIDs{}).linkLine() != "" {
		t.Fatal("empty IDs must render no URLs")
	}
	if got := movie.linkLine(); got != "[TMDB](https://www.themoviedb.org/movie/438631) • "+
		"[IMDb](https://www.imdb.com/title/tt1160419/) • [TVDB](https://thetvdb.com/dereferrer/movie/290)" {
		t.Fatalf("unexpected link line %q", got)
	}
}

func TestOverviewSnippet(t *testing.T) {
	if got := overviewSnippet("  short overview  "); got != "short overview" {
		t.Fatalf("short overviews must pass through trimmed, got %q", got)
	}
	long := strings.Repeat("word ", 200)
	got := overviewSnippet(long)
	if len(got) > discordOverviewLimit {
		t.Fatalf("snippet too long: %d bytes", len(got))
	}
	// Every word in the input is "word": a word-boundary clip must keep the
	// last word whole.
	if !strings.HasSuffix(got, "word…") {
		t.Fatalf("snippet should clip on a word boundary with an ellipsis, got %q", got)
	}
}

func TestRatingAndGenresLabels(t *testing.T) {
	const imdbWin = "★ 8.7 IMDb"
	if got := ratingLabel(8.7, 8.1); got != imdbWin {
		t.Fatalf("IMDb rating must win, got %q", got)
	}
	if got := ratingLabel(0, 8.1); got != "★ 8.1 TMDB" {
		t.Fatalf("TMDB fallback wrong, got %q", got)
	}
	if got := ratingLabel(0, 0); got != "" {
		t.Fatalf("unknown ratings must render nothing, got %q", got)
	}
	if got := genresLabel([]string{"Drama", "", "Sci-Fi & Fantasy", "Mystery", "Thriller"}); got != "Drama, Sci-Fi & Fantasy, Mystery" {
		t.Fatalf("genres must cap at three non-empty entries, got %q", got)
	}
	if got := genresLabel(nil); got != "" {
		t.Fatalf("no genres must render nothing, got %q", got)
	}
}

package catalog

import "testing"

func TestImageTypeFromCachedPathLocalHashedLayout(t *testing.T) {
	// Local keys interpose a content-hash segment BEFORE the image type
	// (local/{contentType}/{contentID}/{hash8}/{imageType}/{variant}.{ext})
	// so the variant's parent directory stays the image type.
	cases := map[string]string{
		"local/movies/movie-1/deadbeef/poster/original.webp":   "poster",
		"local/movies/movie-1/deadbeef/backdrop/original.webp": "backdrop",
		"local/series/show-1/cafef00d/logo/w500.webp":          "logo",
	}
	for path, want := range cases {
		if got := imageTypeFromCachedPath(path); got != want {
			t.Errorf("imageTypeFromCachedPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestImageDeletePrefixTrimsLocalPathsToContentRoot(t *testing.T) {
	// On item deletion, local paths trim to local/{contentType}/{contentID}
	// so every stale hash prefix is swept, not just the live one.
	cases := map[string]string{
		"local/movies/movie-1/deadbeef/poster/original.webp": "local/movies/movie-1/",
		"local/series/show-1/cafef00d/logo/w500.webp":        "local/series/show-1/",
		// Remote cached keys keep the existing per-image-type prefix.
		"tmdb/movies/550/poster/original.webp": "tmdb/movies/550/poster/",
		// Legacy scanner keys without a hash segment stay whole-path dirs.
		"local/ebooks/book-1/poster/original.webp": "local/ebooks/book-1/",
	}
	for path, want := range cases {
		if got := imageDeletePrefix(path); got != want {
			t.Errorf("imageDeletePrefix(%q) = %q, want %q", path, got, want)
		}
	}
}

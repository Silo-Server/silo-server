package catalog

import (
	"strings"
	"testing"
)

func TestImageTypeFromCachedPathPortableLayout(t *testing.T) {
	revision := strings.Repeat("ab", 32)
	cases := map[string]string{
		"artwork/v1/objects/poster/ab/" + revision + "/original.webp": "poster",
		"artwork/v1/objects/backdrop/ab/" + revision + "/w1280.webp":  "backdrop",
		"artwork/v1/objects/still/ab/" + revision + "/manifest.json":  "still",
		"tmdb://poster/abc.jpg": "",
	}
	for path, want := range cases {
		if got := imageTypeFromCachedPath(path); got != want {
			t.Errorf("imageTypeFromCachedPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// Content-addressed artwork is shared: one revision directory can be the
// poster of several items, people, or libraries. Deleting content must hand it
// to reference-aware revision GC instead of sweeping the prefix, which cannot
// tell those references apart.
func TestImageDeletePrefixNeverSweepsPortableArtwork(t *testing.T) {
	revision := strings.Repeat("cd", 32)
	for _, path := range []string{
		"artwork/v1/objects/poster/cd/" + revision + "/original.webp",
		"artwork/v1/objects/backdrop/cd/" + revision + "/w300.webp",
	} {
		if got := imageDeletePrefix(path); got != "" {
			t.Errorf("imageDeletePrefix(%q) = %q, want no sweep", path, got)
		}
	}
}

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

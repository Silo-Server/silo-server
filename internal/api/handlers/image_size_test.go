package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// An explicit size overrides every per-context default uniformly, including the
// Continue Watching w1280 backdrop.
func TestExplicitImageSizeOverridesContextDefaults(t *testing.T) {
	const poster = "tmdb/movies/550/poster/original.abc123.webp"
	const backdrop = "tmdb/movies/550/backdrop/original.abc123.webp"
	const still = "tvdb/series/73141/seasons/22/episodes/9/still/original.webp"

	if got := sizedCardPath(poster, "poster", imagesize.Large); got != "tmdb/movies/550/poster/w780.abc123.webp" {
		t.Errorf("card poster at large = %q", got)
	}
	if got := sizedPosterPath(poster, imagesize.Small); got != "tmdb/movies/550/poster/w300.abc123.webp" {
		t.Errorf("featured poster at small = %q", got)
	}
	if got := sizedPosterPath(poster, imagesize.Original); got != poster {
		t.Errorf("poster at original = %q, want the original key", got)
	}
	if got := sizedBackdropPath(backdrop, imagesize.Small); got != "tmdb/movies/550/backdrop/w300.abc123.webp" {
		t.Errorf("backdrop at small = %q", got)
	}
	for _, sectionType := range []sections.SectionType{sections.SectionContinueWatching, sections.SectionNextUp} {
		if got := sizedSectionBackdropPath(sectionType, backdrop, imagesize.Small); got != "tmdb/movies/550/backdrop/w300.abc123.webp" {
			t.Errorf("%v backdrop at small = %q, want the requested size to win over w1280", sectionType, got)
		}
	}

	// An episode still standing in for a backdrop rides the still ladder, so a
	// backdrop-only width would name a key that was never generated.
	if got := sizedBackdropPath(still, imagesize.Large); got != "tvdb/series/73141/seasons/22/episodes/9/still/w780.webp" {
		t.Errorf("episode still as backdrop at large = %q", got)
	}
}

// Full URLs and plugin-prefixed paths are never rewritten: their variant is
// chosen at resolution time.
func TestExplicitImageSizeLeavesNonCachedPathsAlone(t *testing.T) {
	for _, path := range []string{
		"https://image.tmdb.org/t/p/original/xyz.jpg",
		"plugin://tmdb/movies/550/poster/original.jpg",
	} {
		for _, size := range imagesize.All {
			if got := sizedCardPath(path, "poster", size); got != path {
				t.Errorf("sizedCardPath(%q, %q) = %q, want it unchanged", path, size, got)
			}
		}
	}
}

// An episode row puts its still in the card's backdrop slot. The still ladder
// has no w1920, so resolving that slot against the backdrop ladder would name a
// key the cache never generated — the URL would 404 rather than look wrong.
func TestSizedCardBackdropPathFollowsTheStillLadder(t *testing.T) {
	const still = "tvdb/series/73141/seasons/22/episodes/9/still/original.webp"
	const backdrop = "tmdb/movies/550/backdrop/original.abc123.webp"

	tests := []struct {
		size         imagesize.Size
		wantStill    string
		wantBackdrop string
	}{
		{imagesize.Small, "still/w300.webp", "backdrop/w300.abc123.webp"},
		{imagesize.Medium, "still/w500.webp", "backdrop/w1920.abc123.webp"},
		{imagesize.Large, "still/w780.webp", "backdrop/w1920.abc123.webp"},
		{imagesize.Original, "still/original.webp", "backdrop/original.abc123.webp"},
	}
	for _, tt := range tests {
		t.Run(string(tt.size), func(t *testing.T) {
			if got := sizedCardBackdropPath(still, tt.size); !strings.HasSuffix(got, tt.wantStill) {
				t.Errorf("still in backdrop slot = %q, want it to end in %q", got, tt.wantStill)
			}
			if got := sizedCardBackdropPath(backdrop, tt.size); !strings.HasSuffix(got, tt.wantBackdrop) {
				t.Errorf("real backdrop = %q, want it to end in %q", got, tt.wantBackdrop)
			}
		})
	}
}

func TestRequestImageSizeIgnoresInvalidValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?image_size=enormous", nil)
	if got := requestImageSize(r); got != imagesize.Unset {
		t.Fatalf("requestImageSize = %q, want Unset for an invalid value", got)
	}
}

// A listing card's logo is the file item detail serves: unsized is the medium
// rung, the same key an explicit medium names, never the full original.
func TestSizedLogoPathMatchesItemDetail(t *testing.T) {
	const logo = "tmdb/movies/550/logo/original.png"
	tests := []struct {
		size imagesize.Size
		want string
	}{
		{imagesize.Unset, "tmdb/movies/550/logo/w500.png"},
		{imagesize.Small, "tmdb/movies/550/logo/w500.png"},
		{imagesize.Medium, "tmdb/movies/550/logo/w500.png"},
		{imagesize.Large, "tmdb/movies/550/logo/w1280.png"},
		{imagesize.Original, logo},
	}
	if got := sizedFeaturedLogoPath(logo, imagesize.Unset); got != logo {
		t.Errorf("sizedFeaturedLogoPath(Unset) = %q, want stored path %q", got, logo)
	}
	for _, tt := range tests {
		if got := sizedLogoPath(logo, tt.size); got != tt.want {
			t.Errorf("sizedLogoPath(%q, %q) = %q, want %q", logo, tt.size, got, tt.want)
		}
	}
	for _, path := range []string{"https://image.tmdb.org/t/p/original/xyz.png", "plugin://tmdb/movies/550/logo/original.png", ""} {
		if got := sizedLogoPath(path, imagesize.Unset); got != path {
			t.Errorf("sizedLogoPath(%q, Unset) = %q, want it unchanged", path, got)
		}
	}
}

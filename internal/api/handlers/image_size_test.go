package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/sections"
)

var imageSizePaths = []string{
	"tmdb/movies/550/poster/original.abc123.webp",
	"tmdb/movies/550/backdrop/original.abc123.webp",
	"tvdb/series/73141/seasons/22/episodes/9/still/original.webp",
	"tmdb/movies/550/logo/original.png",
	"https://image.tmdb.org/t/p/original/xyz.jpg",
	"plugin://tmdb/movies/550/poster/original.jpg",
	"",
}

// A request without image_size must produce byte-identical paths to the
// per-context helpers the server has always used. This is the regression guard:
// every existing client sends no parameter.
func TestUnsetImageSizeKeepsExistingPaths(t *testing.T) {
	for _, path := range imageSizePaths {
		if got, want := sizedCardPath(path, "poster", imagesize.Unset), cardThumbnailPath(path); got != want {
			t.Errorf("sizedCardPath(%q, Unset) = %q, want %q", path, got, want)
		}
		if got, want := sizedPosterPath(path, imagesize.Unset), featuredPosterPath(path); got != want {
			t.Errorf("sizedPosterPath(%q, Unset) = %q, want %q", path, got, want)
		}
		if got, want := sizedBackdropPath(path, imagesize.Unset), featuredBackdropPath(path); got != want {
			t.Errorf("sizedBackdropPath(%q, Unset) = %q, want %q", path, got, want)
		}
		for _, sectionType := range []sections.SectionType{sections.SectionContinueWatching, sections.SectionNextUp, sections.SectionRecentlyAdded} {
			got := sizedSectionBackdropPath(sectionType, path, imagesize.Unset)
			want := sectionBackdropPath(sectionType, path)
			if got != want {
				t.Errorf("sizedSectionBackdropPath(%v, %q, Unset) = %q, want %q", sectionType, path, got, want)
			}
		}
	}
}

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

// Absent parameter still means byte-identical output for both kinds of path.
func TestSizedCardBackdropPathUnsetUnchanged(t *testing.T) {
	for _, path := range imageSizePaths {
		if got, want := sizedCardBackdropPath(path, imagesize.Unset), cardThumbnailPath(path); got != want {
			t.Errorf("sizedCardBackdropPath(%q, Unset) = %q, want %q", path, got, want)
		}
	}
}

func TestRequestVariantHint(t *testing.T) {
	if got := requestVariantHint("card", imagesize.Unset); got != "card" {
		t.Errorf("Unset hint = %q, want the caller's card default", got)
	}
	if got := requestVariantHint("featured", imagesize.Unset); got != "featured" {
		t.Errorf("Unset hint = %q, want the caller's featured default", got)
	}
	if got := requestVariantHint("card", imagesize.Original); got != "original" {
		t.Errorf("original hint = %q", got)
	}
	if got := requestVariantHint("featured", imagesize.Small); got != "card" {
		t.Errorf("small hint = %q, want card", got)
	}
	// Large is its own plugin tier, not a synonym for featured: a plugin that
	// can serve a wider image should be told to.
	if got := requestVariantHint("featured", imagesize.Large); got != "large" {
		t.Errorf("large hint = %q, want large", got)
	}
}

func TestRequestImageSizeIgnoresInvalidValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?image_size=enormous", nil)
	if got := requestImageSize(r); got != imagesize.Unset {
		t.Fatalf("requestImageSize = %q, want Unset for an invalid value", got)
	}
}

func TestRejectInvalidImageSize(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if !rejectInvalidImageSize(rec, httptest.NewRequest(http.MethodGet, "/home/sections?image_size=large", nil)) {
			t.Fatal("rejected a valid image_size")
		}
	})

	t.Run("absent", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if !rejectInvalidImageSize(rec, httptest.NewRequest(http.MethodGet, "/home/sections", nil)) {
			t.Fatal("rejected a request with no image_size")
		}
	})

	t.Run("invalid", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if rejectInvalidImageSize(rec, httptest.NewRequest(http.MethodGet, "/home/sections?image_size=huge", nil)) {
			t.Fatal("accepted an invalid image_size")
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "invalid_image_size") {
			t.Fatalf("error body = %s, want an invalid_image_size code", rec.Body.String())
		}
	})
}

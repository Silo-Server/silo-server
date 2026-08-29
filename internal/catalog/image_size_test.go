package catalog

import "testing"

// The literals below are the variants this package returned before the
// image_size parameter existed. Routing the size hint through
// internal/imagesize must not move any of them: an absent parameter, and the
// "small"/"original" literals the existing call sites already pass, have to
// keep resolving to exactly the same object keys.
func TestCachedImageVariantKeyMatchesPreImageSizeBehavior(t *testing.T) {
	want := map[string]map[string]string{
		"": {
			"backdrop": "w1920",
			"logo":     "w500",
			"poster":   "w500",
			"still":    "w500",
			"profile":  "w500",
			"mystery":  "w500",
		},
		"small": {
			"backdrop": "w300",
			"logo":     "w500",
			"poster":   "w300",
			"still":    "w300",
			"profile":  "w300",
			"mystery":  "w300",
		},
		"medium": {
			"backdrop": "w1920",
			"logo":     "w500",
			"poster":   "w500",
			"still":    "w500",
			"profile":  "w500",
			"mystery":  "w500",
		},
		"original": {
			"backdrop": "original",
			"logo":     "original",
			"poster":   "original",
			"still":    "original",
			"profile":  "original",
			"mystery":  "original",
		},
	}

	for size, byType := range want {
		for imageType, wantVariant := range byType {
			if got := cachedImageVariantKey(imageType, size); got != wantVariant {
				t.Errorf("cachedImageVariantKey(%q, %q) = %q, want %q", imageType, size, got, wantVariant)
			}
		}
	}
}

// "large" is the one genuinely new size; it must reach the widest cached rung
// short of the original.
func TestCachedImageVariantKeyLarge(t *testing.T) {
	want := map[string]string{
		"backdrop": "w1920",
		"logo":     "w1280",
		"poster":   "w780",
		"still":    "w780",
		"profile":  "w500",
	}
	for imageType, wantVariant := range want {
		if got := cachedImageVariantKey(imageType, "large"); got != wantVariant {
			t.Errorf("cachedImageVariantKey(%q, large) = %q, want %q", imageType, got, wantVariant)
		}
	}
}

// An unparseable hint must degrade to the historical default rather than
// producing an empty variant, which would leave the object key on "original".
func TestCachedImageVariantKeyUnknownSize(t *testing.T) {
	if got := cachedImageVariantKey("poster", "gigantic"); got != "w500" {
		t.Fatalf("cachedImageVariantKey(poster, gigantic) = %q, want w500", got)
	}
}

func TestSizeToVariant(t *testing.T) {
	want := map[string]string{
		"":         "featured",
		"small":    "card",
		"medium":   "featured",
		"large":    "large",
		"original": "original",
		"nonsense": "featured",
	}
	for size, wantVariant := range want {
		if got := sizeToVariant(size); got != wantVariant {
			t.Errorf("sizeToVariant(%q) = %q, want %q", size, got, wantVariant)
		}
	}
}

func TestCachedImageVariantPathRewritesKey(t *testing.T) {
	const original = "tmdb/movies/550/poster/original.abc123.webp"
	tests := map[string]string{
		"":         "tmdb/movies/550/poster/w500.abc123.webp",
		"small":    "tmdb/movies/550/poster/w300.abc123.webp",
		"medium":   "tmdb/movies/550/poster/w500.abc123.webp",
		"large":    "tmdb/movies/550/poster/w780.abc123.webp",
		"original": original,
	}
	for size, wantPath := range tests {
		if got := cachedImageVariantPath(original, "poster", size); got != wantPath {
			t.Errorf("cachedImageVariantPath(%q, poster, %q) = %q, want %q", original, size, got, wantPath)
		}
	}

	const pluginPath = "plugin://tmdb/poster/original.webp"
	if got := cachedImageVariantPath(pluginPath, "poster", "large"); got != pluginPath {
		t.Errorf("cachedImageVariantPath(plugin path) = %q, want it unchanged", got)
	}
}

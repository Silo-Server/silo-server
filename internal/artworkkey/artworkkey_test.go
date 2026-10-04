package artworkkey

import "testing"

func TestRevisionedArtworkKeys(t *testing.T) {
	base := "tmdb/movies/550/poster"
	original := Original(base, "abc123", ".webp")
	if original != base+"/original.abc123.webp" {
		t.Fatalf("Original() = %q", original)
	}
	if got := Variant(original, "w500"); got != base+"/w500.abc123.webp" {
		t.Fatalf("Variant() = %q", got)
	}
	if got := Revision(original); got != "abc123" {
		t.Fatalf("Revision() = %q", got)
	}
	if got := Directory(original); got != base+"/" {
		t.Fatalf("Directory() = %q", got)
	}
}

func TestLegacyArtworkKeysRemainSupported(t *testing.T) {
	original := "tmdb/movies/550/poster/original.webp"
	if got := Variant(original, "w300"); got != "tmdb/movies/550/poster/w300.webp" {
		t.Fatalf("Variant() = %q", got)
	}
	if got := Revision(original); got != "" {
		t.Fatalf("Revision() = %q, want empty", got)
	}
}

func TestVariantOnlyRewritesOriginalFilename(t *testing.T) {
	original := "tmdb/movies/original.segment/550/poster/original.abc123.webp"
	want := "tmdb/movies/original.segment/550/poster/w500.abc123.webp"
	if got := Variant(original, "w500"); got != want {
		t.Fatalf("Variant() = %q, want %q", got, want)
	}
}

func TestOriginalOfNamesTheRevisionsOriginal(t *testing.T) {
	for key, want := range map[string]string{
		"tmdb/movies/550/poster/w500.abc123.webp":     "tmdb/movies/550/poster/original.abc123.webp",
		"tmdb/movies/550/poster/original.abc123.webp": "tmdb/movies/550/poster/original.abc123.webp",
		"tmdb/movies/550/poster/w500.webp":            "tmdb/movies/550/poster/w500.webp",
		"w500.abc123.webp":                            "w500.abc123.webp",
	} {
		if got := OriginalOf(key); got != want {
			t.Errorf("OriginalOf(%q) = %q, want %q", key, got, want)
		}
	}
}

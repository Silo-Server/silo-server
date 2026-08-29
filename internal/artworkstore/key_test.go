package artworkstore

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateKeyAccepts pins the shapes real callers produce: the portable
// artwork/v1 grammar, the legacy provider-oriented keys that must stay
// readable, and the upload-surface keys that move onto the store.
func TestValidateKeyAccepts(t *testing.T) {
	keys := []string{
		"artwork/v1/objects/poster/ab/" + strings.Repeat("a", 64) + "/original.webp",
		"artwork/v1/objects/backdrop/0f/" + strings.Repeat("f", 64) + "/w1280.webp",
		"artwork/v1/objects/poster/ab/" + strings.Repeat("a", 64) + "/manifest.json",
		"tmdb/movie/movie-tmdb-228064/poster/original.9f8e7d.webp",
		"tmdb/series/series-tvdb-121361/localizations/en-US/seasons/1/episodes/2/still/w300.abc.webp",
		"library-posters/17.jpg",
		"collection-images/0f2a1c3e-4d5b-6a7b-8c9d-0e1f2a3b4c5d/poster/original.webp",
		"branding/mark/9f8e7d6c5b4a3210.png",
		"single-component-key.webp",
	}
	for _, key := range keys {
		if err := ValidateKey(key); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", key, err)
		}
	}
}

// TestValidateKeyRejects covers every traversal, escape, and reserved-name
// shape. ValidateKey is the single gate in front of both backends, so a gap
// here is a gap everywhere.
func TestValidateKeyRejects(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"absolute", "/etc/passwd"},
		{"absolute root", "/"},
		{"parent traversal", "../outside/secret"},
		{"embedded traversal", "artwork/v1/../../outside/secret"},
		{"trailing traversal", "artwork/v1/objects/.."},
		{"current dir component", "artwork/./v1/original.webp"},
		{"bare current dir", "."},
		{"double separator", "artwork//v1/original.webp"},
		{"trailing separator", "artwork/v1/"},
		{"leading separator only", "//artwork"},
		{"backslash", `artwork\v1\original.webp`},
		{"windows absolute", `C:\artwork\original.webp`},
		{"nul byte", "artwork/v1\x00/original.webp"},
		{"newline", "artwork/v1\n/original.webp"},
		{"space", "artwork/v 1/original.webp"},
		{"tab", "artwork/v1/orig\tinal.webp"},
		{"non ascii", "artwork/v1/póster.webp"},
		{"scheme", "s3://bucket/artwork/original.webp"},
		{"url", "https://example.invalid/poster.jpg"},
		{"dot file component", ".silo-artwork-store"},
		{"nested dot file", "artwork/v1/.silo-artwork-store"},
		{"temp file", tempFilePrefix + "0123456789ab"},
		{"nested temp file", "artwork/v1/" + tempFilePrefix + "0123456789ab"},
		{"overlong component", "artwork/" + strings.Repeat("a", MaxKeyComponentLength+1) + "/original.webp"},
		{"overlong key", strings.Repeat("ab/", MaxKeyLength/3) + "original.webp"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKey(tc.key)
			if err == nil {
				t.Fatalf("ValidateKey(%q) = nil, want ErrInvalidKey", tc.key)
			}
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("ValidateKey(%q) = %v, want ErrInvalidKey", tc.key, err)
			}
		})
	}
}

func TestMediaTypeForKey(t *testing.T) {
	tests := map[string]string{
		"artwork/v1/objects/poster/ab/rev/original.webp": "image/webp",
		"artwork/v1/objects/poster/ab/rev/w500.JPG":      "image/jpeg",
		"a/b.jpeg":        "image/jpeg",
		"a/b.png":         "image/png",
		"a/b.gif":         "image/gif",
		"a/b.avif":        "image/avif",
		"a/b.svg":         "image/svg+xml",
		"a/manifest.json": "application/json",
		"a/b.bin":         "",
		"a/b":             "",
	}
	for key, want := range tests {
		if got := MediaTypeForKey(key); got != want {
			t.Errorf("MediaTypeForKey(%q) = %q, want %q", key, got, want)
		}
	}
}

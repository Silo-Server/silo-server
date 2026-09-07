package metadata

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type manifestReader map[string]ArtworkAvailability

func (m manifestReader) ArtworkAvailability(context.Context, []string) (map[string]ArtworkAvailability, error) {
	return m, nil
}

type noProbeStore struct{ t *testing.T }

func (s noProbeStore) Bucket() string { return "media" }
func (s noProbeStore) PresignGetURL(_ context.Context, _, key string, _ time.Duration) (string, error) {
	return "https://images.example/" + key, nil
}
func (s noProbeStore) ObjectExists(context.Context, string, string) (bool, error) {
	s.t.Fatal("catalog read checked storage")
	return false, nil
}
func (s noProbeStore) ObjectAvailable(context.Context, string, string) (bool, error) {
	s.t.Fatal("catalog read checked delivery")
	return false, nil
}
func (s noProbeStore) UsesExternalDelivery() bool { return true }

func TestCatalogArtworkColdReadsNeverProbe(t *testing.T) {
	// Recreate both resolver caches on every iteration, including 37 distinct
	// season posters. A store whose probe methods fail proves independence
	// from slow, missing, or unavailable delivery without a timing threshold.
	for range 2 {
		resolver := NewPluginImageResolver()
		t.Cleanup(resolver.Close)
		resolver.SetS3Presigner(noProbeStore{t}, time.Hour)
		manifest := manifestReader{}
		var paths []string
		for i := range 37 {
			key := fmt.Sprintf("tmdb/series/1/seasons/%d/poster/w780.rev.webp", i)
			paths = append(paths, key)
			original := variantKey(key, "original")
			manifest[original] = ArtworkAvailability{Published: []string{key, variantKey(key, "w500"), original}, External: true}
		}
		resolver.SetArtworkAvailabilityReader(manifest)
		urls := resolver.ResolveImageURLs(t.Context(), paths, "large")
		if len(urls) != 37 {
			t.Fatalf("got %d URLs", len(urls))
		}
		for _, key := range paths {
			if !strings.HasSuffix(urls[key], variantKey(key, "w500")) {
				t.Fatalf("unverified variant advertised: %s", urls[key])
			}
		}
	}
}

func TestSelectPublishedVariant(t *testing.T) {
	const key = "tmdb/series/1/poster/w780.rev.webp"
	medium, small, original := variantKey(key, "w500"), variantKey(key, "w300"), variantKey(key, "original")
	for _, tc := range []struct {
		name  string
		state ArtworkAvailability
		known bool
		want  string
	}{
		{"legacy", ArtworkAvailability{}, false, medium},
		{"published", ArtworkAvailability{Published: []string{key, original}}, true, key},
		{"missing large", ArtworkAvailability{Published: []string{small, original}}, true, small},
		{"original only", ArtworkAvailability{Published: []string{original}}, true, original},
		{"delivery verified", ArtworkAvailability{Published: []string{key, medium, original}, External: true, Verified: true, Deliverable: []string{key}}, true, key},
		{"delivery missing", ArtworkAvailability{Published: []string{key, medium, original}, External: true, Verified: true, Deliverable: []string{medium}}, true, medium},
		{"delivery down", ArtworkAvailability{Published: []string{key, original}, External: true, Verified: true}, true, ""},
		{"unpublished", ArtworkAvailability{}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectPublishedVariant(key, tc.state, tc.known); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestVariantKeyRebuild(t *testing.T) {
	tests := []struct {
		key     string
		variant string
		want    string
	}{
		{"tmdb/movies/550/poster/w780.abc123.webp", "w500", "tmdb/movies/550/poster/w500.abc123.webp"},
		{"tmdb/movies/550/poster/w780.abc123.webp", "original", "tmdb/movies/550/poster/original.abc123.webp"},
		{"tvdb/series/1/still/w780.webp", "w300", "tvdb/series/1/still/w300.webp"},
		{"w780.webp", "w300", "w780.webp"},
	}
	for _, tt := range tests {
		if got := variantKey(tt.key, tt.variant); got != tt.want {
			t.Errorf("variantKey(%q, %q) = %q, want %q", tt.key, tt.variant, got, tt.want)
		}
	}
}

func TestKeyVariant(t *testing.T) {
	tests := map[string]string{
		"tmdb/movies/550/poster/w780.abc123.webp": "w780",
		"tvdb/series/1/still/w300.webp":           "w300",
		"tmdb/movies/550/poster/original.webp":    "original",
	}
	for key, want := range tests {
		if got := keyVariant(key); got != want {
			t.Errorf("keyVariant(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestOriginalArtworkFallsBackToResizedVariant(t *testing.T) {
	original := "tmdb/movies/550/poster/original.abc123.webp"
	large, medium := variantKey(original, "w780"), variantKey(original, "w500")
	for _, tc := range []struct {
		name string
		keys []string
		want string
	}{
		{"original preferred", []string{original, large}, original},
		{"largest survivor", []string{medium, large}, large},
		{"lower survivor", []string{medium}, medium},
		{"none available", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, verified := range []bool{false, true} {
				state := ArtworkAvailability{Published: tc.keys}
				if verified {
					state = ArtworkAvailability{Published: []string{original, large, medium}, External: true, Verified: true, Deliverable: tc.keys}
				}
				if got := selectPublishedVariant(original, state, true); got != tc.want {
					t.Fatalf("verified=%v: got %q, want %q", verified, got, tc.want)
				}
			}
		})
	}
}

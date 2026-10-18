package metadata

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type manifestReader map[string]ArtworkAvailability

func (m manifestReader) ArtworkAvailability(context.Context, []string) (map[string]ArtworkAvailability, error) {
	return m, nil
}

type noProbeStore struct{ t *testing.T }

func (s noProbeStore) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		out[key] = catalog.ResolvedImageURL{URL: "https://images.example/" + key}
	}
	return out
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
		resolver.SetArtworkResolver(noProbeStore{t})
		manifest := manifestReader{}
		var paths []string
		for i := range 37 {
			key := fmt.Sprintf("tmdb/series/1/seasons/%d/poster/w1000.rev.webp", i)
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
	key := "tmdb/series/1/poster/" + posterLargeRung + ".rev.webp"
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
		// A revision cached under an earlier ladder serves its own widest rung
		// that fits, not just the current ladder's.
		{"previous ladder's rung", ArtworkAvailability{Published: []string{variantKey(key, "w780"), medium, small, original}}, true, variantKey(key, "w780")},
		{"previous ladder's rung too wide", ArtworkAvailability{Published: []string{variantKey(key, "w1280"), medium, original}}, true, medium},
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

// Artwork without a manifest was cached before the ladder grew, so any rung
// above the type's medium clamps to medium however many rungs were added.
func TestLegacyArtworkClampsToEstablishedRung(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"tmdb/movies/1/poster/w1000.webp", "tmdb/movies/1/poster/w500.webp"},
		{"tmdb/movies/1/poster/w780.webp", "tmdb/movies/1/poster/w500.webp"},
		{"tmdb/movies/1/poster/w300.webp", "tmdb/movies/1/poster/w300.webp"},
		{"tmdb/series/1/seasons/1/episodes/1/still/w1280.webp", "tmdb/series/1/seasons/1/episodes/1/still/w500.webp"},
		{"tmdb/movies/1/backdrop/w2560.webp", "tmdb/movies/1/backdrop/w1920.webp"},
		{"tmdb/movies/1/backdrop/w1280.webp", "tmdb/movies/1/backdrop/w1280.webp"},
		{"tmdb/movies/1/logo/w1280.webp", "tmdb/movies/1/logo/w500.webp"},
		{"tmdb/movies/1/backdrop/original.webp", "tmdb/movies/1/backdrop/original.webp"},
		{"tmdb/movies/1/profile/w500.webp", "tmdb/movies/1/profile/w500.webp"},
	} {
		if got := selectPublishedVariant(tc.key, ArtworkAvailability{}, false); got != tc.want {
			t.Errorf("selectPublishedVariant(%q, legacy) = %q, want %q", tc.key, got, tc.want)
		}
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

func TestOriginalArtworkFallsBackToResizedVariant(t *testing.T) {
	original := "tmdb/movies/550/poster/original.abc123.webp"
	large, medium := variantKey(original, "w1000"), variantKey(original, "w500")
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

func TestStoredKeysResolveDirectlyWithoutAvailabilityReader(t *testing.T) {
	// Without publication metadata the catalog asks for exactly the key it
	// holds.
	resolver := NewPluginImageResolver()
	t.Cleanup(resolver.Close)
	resolver.SetArtworkResolver(noProbeStore{t})
	key := "tmdb/series/1/seasons/3/poster/" + posterLargeRung + ".rev.webp"
	if got := resolver.ResolveImageURL(t.Context(), key, "large"); got != "https://images.example/"+key {
		t.Fatalf("stored key was rewritten: %s", got)
	}
}

type recordingAvailability struct {
	external bool
	states   map[string]ArtworkAvailability
	asked    []string
}

func (r *recordingAvailability) ArtworkAvailability(_ context.Context, paths []string) (map[string]ArtworkAvailability, error) {
	r.asked = append(r.asked, paths...)
	return r.states, nil
}

func (r *recordingAvailability) ExternalDelivery() bool { return r.external }

// Where delivery cannot lag behind a write, only a rung a ladder change added
// can be missing from a published revision, so established rungs resolve
// without a lookup and only the added rung consults the manifest.
func TestNonExternalReaderLooksUpOnlyAddedRungs(t *testing.T) {
	medium := "tmdb/movies/1/poster/w500.rev.webp"
	large := "tmdb/movies/2/poster/" + posterLargeRung + ".rev.webp"
	reader := &recordingAvailability{states: map[string]ArtworkAvailability{
		variantKey(large, "original"): {Published: []string{variantKey(large, "w500"), variantKey(large, "original")}},
	}}
	got := resolvePublishedLadderKeys(t.Context(), reader, []resolveEntry{{originalPath: medium}, {originalPath: large}})
	if !slices.Equal(reader.asked, []string{variantKey(large, "original")}) {
		t.Fatalf("looked up %v, want only the added rung's revision", reader.asked)
	}
	if got[medium] != medium {
		t.Errorf("established rung resolved to %q, want itself", got[medium])
	}
	if want := variantKey(large, "w500"); got[large] != want {
		t.Errorf("added rung resolved to %q, want the published %q", got[large], want)
	}

	// External delivery can lag behind any write, so every key is looked up.
	external := &recordingAvailability{external: true}
	resolvePublishedLadderKeys(t.Context(), external, []resolveEntry{{originalPath: medium}, {originalPath: large}})
	if len(external.asked) != 2 {
		t.Fatalf("external reader looked up %v, want both revisions", external.asked)
	}
}

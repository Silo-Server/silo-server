package metadata

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3ImageStore is a presigner that also answers existence checks, standing
// in for *s3client.Client.
type fakeS3ImageStore struct {
	mu       sync.Mutex
	existing map[string]bool
	err      error
	checks   []string
	presigns []string
}

func newFakeS3ImageStore(existing ...string) *fakeS3ImageStore {
	store := &fakeS3ImageStore{existing: map[string]bool{}}
	for _, key := range existing {
		store.existing[key] = true
	}
	return store
}

func (s *fakeS3ImageStore) Bucket() string { return "media" }

func (s *fakeS3ImageStore) PresignGetURL(_ context.Context, bucket, key string, _ time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presigns = append(s.presigns, key)
	return "https://s3.example.com/" + bucket + "/" + key, nil
}

func (s *fakeS3ImageStore) ObjectExists(_ context.Context, _ string, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks = append(s.checks, key)
	if s.err != nil {
		return false, s.err
	}
	return s.existing[key], nil
}

func (s *fakeS3ImageStore) checkCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.checks)
}

func newResolverWithStore(t *testing.T, store *fakeS3ImageStore) *PluginImageResolver {
	t.Helper()
	resolver := NewPluginImageResolver()
	t.Cleanup(resolver.Close)
	resolver.SetS3Presigner(store, 30*time.Minute)
	return resolver
}

func resolvedKey(t *testing.T, resolver *PluginImageResolver, path string) string {
	t.Helper()
	got := resolver.ResolveImageURLWithExpiry(context.Background(), path, "featured")
	if got.URL == "" {
		t.Fatalf("resolving %q returned no URL", path)
	}
	return strings.TrimPrefix(got.URL, "https://s3.example.com/media/")
}

func TestLadderFallbackServesRequestedRungWhenPresent(t *testing.T) {
	const key = "tmdb/movies/550/poster/w780.abc123.webp"
	store := newFakeS3ImageStore(key)
	resolver := newResolverWithStore(t, store)

	if got := resolvedKey(t, resolver, key); got != key {
		t.Fatalf("resolved key = %q, want the requested rung %q", got, key)
	}
}

func TestLadderFallbackWalksDownToAnExistingRung(t *testing.T) {
	const requested = "tmdb/movies/550/poster/w780.abc123.webp"
	const present = "tmdb/movies/550/poster/w500.abc123.webp"
	store := newFakeS3ImageStore(present)
	resolver := newResolverWithStore(t, store)

	if got := resolvedKey(t, resolver, requested); got != present {
		t.Fatalf("resolved key = %q, want the next lower rung %q", got, present)
	}
}

func TestLadderFallbackEndsAtTheOriginal(t *testing.T) {
	const requested = "tvdb/series/73141/seasons/22/episodes/9/still/w780.webp"
	store := newFakeS3ImageStore() // nothing exists
	resolver := newResolverWithStore(t, store)

	want := "tvdb/series/73141/seasons/22/episodes/9/still/original.webp"
	if got := resolvedKey(t, resolver, requested); got != want {
		t.Fatalf("resolved key = %q, want the original as the terminal fallback %q", got, want)
	}
	// The original is never checked — it predates every rung.
	for _, checked := range store.checks {
		if strings.Contains(checked, "/original.") {
			t.Fatalf("checked %q; the original should be the unchecked terminal fallback", checked)
		}
	}
}

func TestLadderFallbackWalksLogoLadder(t *testing.T) {
	const requested = "tmdb/series/1396/logo/w1280.rev.webp"
	const present = "tmdb/series/1396/logo/w500.rev.webp"
	store := newFakeS3ImageStore(present)
	resolver := newResolverWithStore(t, store)

	if got := resolvedKey(t, resolver, requested); got != present {
		t.Fatalf("resolved key = %q, want %q", got, present)
	}
}

// A rung that has always existed must not cost a HEAD request.
func TestLadderFallbackSkipsEstablishedRungs(t *testing.T) {
	store := newFakeS3ImageStore()
	resolver := newResolverWithStore(t, store)

	for _, key := range []string{
		"tmdb/movies/550/poster/w500.abc123.webp",
		"tmdb/movies/550/poster/w300.abc123.webp",
		"tmdb/movies/550/backdrop/w1920.abc123.webp",
		"tmdb/movies/550/poster/original.abc123.webp",
		"tmdb/series/1396/logo/w500.rev.webp",
	} {
		if got := resolvedKey(t, resolver, key); got != key {
			t.Errorf("resolved key = %q, want %q untouched", got, key)
		}
	}
	if store.checkCount() != 0 {
		t.Fatalf("existence checks = %v, want none for established rungs", store.checks)
	}
}

// Storage being briefly unreachable must not downgrade artwork, and must not be
// remembered as absence.
func TestLadderFallbackPresignsRequestedKeyOnCheckError(t *testing.T) {
	const requested = "tmdb/movies/550/poster/w780.abc123.webp"
	store := newFakeS3ImageStore()
	store.err = errors.New("s3 unavailable")
	resolver := newResolverWithStore(t, store)

	if got := resolvedKey(t, resolver, requested); got != requested {
		t.Fatalf("resolved key = %q, want the requested rung presigned optimistically", got)
	}

	// Nothing was cached, so a later request checks again rather than inheriting
	// a phantom "missing".
	store.err = nil
	store.existing[requested] = true
	resolver.urlCache.InvalidatePrefix("")
	if got := resolvedKey(t, resolver, requested); got != requested {
		t.Fatalf("resolved key after recovery = %q, want %q", got, requested)
	}
	if store.checkCount() < 2 {
		t.Fatalf("existence checks = %v, want the failed check not to have been cached", store.checks)
	}
}

func TestLadderFallbackCachesExistenceAnswers(t *testing.T) {
	const requested = "tmdb/movies/550/poster/w780.abc123.webp"
	const present = "tmdb/movies/550/poster/w500.abc123.webp"
	store := newFakeS3ImageStore(present)
	resolver := newResolverWithStore(t, store)

	resolvedKey(t, resolver, requested)
	first := store.checkCount()
	resolver.urlCache.InvalidatePrefix("")
	resolvedKey(t, resolver, requested)

	if store.checkCount() != first {
		t.Fatalf("existence checks = %d, want the first %d answers reused from cache", store.checkCount(), first)
	}
}

// A fallback URL must leave the resolved-URL cache about as soon as the real
// rung could have been backfilled, not a full presign window later.
func TestLadderFallbackClampsResolvedURLLifetime(t *testing.T) {
	const requested = "tmdb/movies/550/poster/w780.abc123.webp"
	const present = "tmdb/movies/550/poster/w500.abc123.webp"
	store := newFakeS3ImageStore(present)
	resolver := newResolverWithStore(t, store)

	fallback := resolver.ResolveImageURLWithExpiry(context.Background(), requested, "featured")
	if fallback.ExpiresAt == nil {
		t.Fatal("fallback URL has no expiry")
	}
	if got := time.Until(*fallback.ExpiresAt); got > missingExistsCacheTTL+resolvedURLCacheSafetyMargin {
		t.Fatalf("fallback expiry in %v, want at most %v", got, missingExistsCacheTTL+resolvedURLCacheSafetyMargin)
	}

	// A key served at the rung that was asked for keeps the full window.
	direct := resolver.ResolveImageURLWithExpiry(context.Background(), present, "featured")
	if direct.ExpiresAt == nil {
		t.Fatal("direct URL has no expiry")
	}
	if got := time.Until(*direct.ExpiresAt); got <= missingExistsCacheTTL+resolvedURLCacheSafetyMargin {
		t.Fatalf("direct expiry in %v, want the full presign window", got)
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

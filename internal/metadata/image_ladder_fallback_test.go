package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3ImageStore is a presigner that also answers existence checks, standing
// in for *s3client.Client.
type fakeS3ImageStore struct {
	mu               sync.Mutex
	existing         map[string]bool
	deliverable      map[string]bool
	deliveryErrors   map[string]error
	externalDelivery bool
	err              error
	checks           []string
	deliveryChecks   []string
	presigns         []string
	deliveryStarted  chan<- string
	deliveryRelease  <-chan struct{}
}

func newFakeS3ImageStore(existing ...string) *fakeS3ImageStore {
	store := &fakeS3ImageStore{existing: map[string]bool{}}
	for _, key := range existing {
		store.existing[key] = true
	}
	return store
}

func (s *fakeS3ImageStore) Bucket() string { return "media" }

func (s *fakeS3ImageStore) UsesExternalDelivery() bool { return s.externalDelivery }

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

func (s *fakeS3ImageStore) ObjectAvailable(ctx context.Context, _ string, key string) (bool, error) {
	s.mu.Lock()
	s.deliveryChecks = append(s.deliveryChecks, key)
	started := s.deliveryStarted
	release := s.deliveryRelease
	err := s.deliveryErrors[key]
	if err == nil {
		err = s.err
	}
	deliverable := s.deliverable[key]
	s.mu.Unlock()

	if started != nil {
		select {
		case started <- key:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if err != nil {
		return false, err
	}
	return deliverable, nil
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

func TestLadderFallbackWalksBackdropLadder(t *testing.T) {
	const requested = "tmdb/movies/550/backdrop/w1920.rev.webp"
	const present = "tmdb/movies/550/backdrop/w1280.rev.webp"
	store := newFakeS3ImageStore(present)
	resolver := newResolverWithStore(t, store)

	if got := resolvedKey(t, resolver, requested); got != present {
		t.Fatalf("resolved key = %q, want %q", got, present)
	}
}

// A storage HEAD is not proof that a separately authenticated public URL is
// fetchable. The ladder must check the same delivery path the client will use.
func TestLadderFallbackUsesClientDeliveryAvailability(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		present   string
	}{
		{
			name:      "poster",
			requested: "tmdb/movies/550/poster/w780.rev.webp",
			present:   "tmdb/movies/550/poster/w500.rev.webp",
		},
		{
			name:      "still",
			requested: "tmdb/series/1396/seasons/1/episodes/1/still/w780.rev.webp",
			present:   "tmdb/series/1396/seasons/1/episodes/1/still/w500.rev.webp",
		},
		{
			name:      "logo",
			requested: "tmdb/series/1396/logo/w1280.rev.webp",
			present:   "tmdb/series/1396/logo/w500.rev.webp",
		},
		{
			name:      "backdrop",
			requested: "tmdb/movies/550/backdrop/w1920.rev.webp",
			present:   "tmdb/movies/550/backdrop/w1280.rev.webp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeS3ImageStore(tt.requested, tt.present)
			store.externalDelivery = true
			store.deliverable = map[string]bool{tt.present: true}
			resolver := newResolverWithStore(t, store)

			if got := resolvedKey(t, resolver, tt.requested); got != tt.present {
				t.Fatalf("resolved key = %q, want client-fetchable rung %q", got, tt.present)
			}
			if len(store.deliveryChecks) == 0 || store.deliveryChecks[0] != tt.requested {
				t.Fatalf("delivery checks = %v, want requested rung checked first", store.deliveryChecks)
			}
			if len(store.checks) != 0 {
				t.Fatalf("storage checks = %v, want client-delivery checks", store.checks)
			}
		})
	}
}

func TestLadderFallbackRechecksExternalDeliveryForPromotion(t *testing.T) {
	const requested = "tmdb/movies/550/poster/w780.rev.webp"
	const present = "tmdb/movies/550/poster/w500.rev.webp"
	store := newFakeS3ImageStore(requested, present)
	store.externalDelivery = true
	store.deliverable = map[string]bool{present: true}
	resolver := newResolverWithStore(t, store)

	if got := resolvedKey(t, resolver, requested); got != present {
		t.Fatalf("initial resolved key = %q, want fallback %q", got, present)
	}

	store.mu.Lock()
	store.deliverable[requested] = true
	store.mu.Unlock()
	// Expiry drives these invalidations in production; do it directly so the
	// unit test proves promotion without waiting for the real TTL.
	resolver.existsCache.InvalidatePrefix("")
	resolver.urlCache.InvalidatePrefix("")

	if got := resolvedKey(t, resolver, requested); got != requested {
		t.Fatalf("resolved key after delivery recovery = %q, want %q", got, requested)
	}
}

func TestLadderFallbackUsesLowerRungWhenDeliveryCheckErrors(t *testing.T) {
	const requested = "tmdb/movies/550/poster/w780.rev.webp"
	const present = "tmdb/movies/550/poster/w500.rev.webp"
	store := newFakeS3ImageStore(requested, present)
	store.externalDelivery = true
	store.deliverable = map[string]bool{present: true}
	store.deliveryErrors = map[string]error{requested: errors.New("delivery unavailable")}
	resolver := newResolverWithStore(t, store)

	if got := resolvedKey(t, resolver, requested); got != present {
		t.Fatalf("resolved key = %q, want lower fetchable rung %q", got, present)
	}
	if len(store.deliveryChecks) != 1 || store.deliveryChecks[0] != requested {
		t.Fatalf("delivery checks = %v, want one bounded check of requested rung", store.deliveryChecks)
	}
}

func TestLadderFallbackStopsDeliveryProbesAfterBatchFailure(t *testing.T) {
	const entryCount = ladderExistenceConcurrency * 4

	started := make(chan string, entryCount)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)

	store := newFakeS3ImageStore()
	store.externalDelivery = true
	store.err = errors.New("delivery unavailable")
	store.deliveryStarted = started
	store.deliveryRelease = release
	resolver := newResolverWithStore(t, store)

	entries := make([]resolveEntry, 0, entryCount)
	for i := range entryCount {
		entries = append(entries, resolveEntry{
			originalPath: fmt.Sprintf("tmdb/movies/%d/poster/w780.rev.webp", i),
		})
	}

	resolved := make(chan map[string]ladderKey, 1)
	go func() {
		resolved <- resolver.resolveLadderKeys(t.Context(), availabilityPolicy(store), store.Bucket(), entries)
	}()

	waitCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for range ladderExistenceConcurrency {
		select {
		case <-started:
		case <-waitCtx.Done():
			t.Fatalf("delivery probes started = %d, want initial worker set of %d", len(started), ladderExistenceConcurrency)
		}
	}
	select {
	case key := <-started:
		t.Fatalf("delivery probe %q exceeded the %d-worker limit before release", key, ladderExistenceConcurrency)
	default:
	}

	unblock()
	var got map[string]ladderKey
	select {
	case got = <-resolved:
	case <-waitCtx.Done():
		t.Fatal("ladder batch did not fall back after delivery failure")
	}

	store.mu.Lock()
	deliveryChecks := append([]string(nil), store.deliveryChecks...)
	store.mu.Unlock()
	if len(deliveryChecks) > ladderExistenceConcurrency {
		t.Fatalf("delivery checks = %d, want at most the initial worker set of %d", len(deliveryChecks), ladderExistenceConcurrency)
	}
	for _, entry := range entries {
		want := strings.Replace(entry.originalPath, "/w780.", "/w500.", 1)
		if result := got[entry.originalPath]; result.key != want || !result.fellBack {
			t.Errorf("resolved[%q] = %#v, want fallback key %q", entry.originalPath, result, want)
		}
	}
}

// A rung that has always existed must not cost a HEAD request.
func TestLadderFallbackSkipsEstablishedRungs(t *testing.T) {
	store := newFakeS3ImageStore()
	resolver := newResolverWithStore(t, store)

	for _, key := range []string{
		"tmdb/movies/550/poster/w500.abc123.webp",
		"tmdb/movies/550/poster/w300.abc123.webp",
		"tmdb/movies/550/backdrop/w1280.abc123.webp",
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

func TestLadderFallbackClampsExternallyVerifiedURLLifetime(t *testing.T) {
	const requested = "tmdb/movies/550/poster/w780.abc123.webp"
	store := newFakeS3ImageStore(requested)
	store.externalDelivery = true
	store.deliverable = map[string]bool{requested: true}
	resolver := newResolverWithStore(t, store)

	resolved := resolver.ResolveImageURLWithExpiry(t.Context(), requested, "featured")
	if resolved.ExpiresAt == nil {
		t.Fatal("externally verified URL has no expiry")
	}
	if got := time.Until(*resolved.ExpiresAt); got > externalAvailabilityCacheTTL+resolvedURLCacheSafetyMargin {
		t.Fatalf("externally verified expiry in %v, want at most %v", got, externalAvailabilityCacheTTL+resolvedURLCacheSafetyMargin)
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

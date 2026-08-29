package metadata

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeExpiringImageSource struct {
	expiresAt *time.Time
	delay     time.Duration
	calls     atomic.Int32
}

func (s *fakeExpiringImageSource) ResolveImageURL(ctx context.Context, path string, variant string) (string, error) {
	resolved, err := s.ResolveImageURLWithExpiry(ctx, path, variant)
	return resolved.URL, err
}

func (s *fakeExpiringImageSource) ResolveImageURLWithExpiry(ctx context.Context, path string, variant string) (catalog.ResolvedImageURL, error) {
	resolved, err := s.ResolveImageURLsWithExpiry(ctx, []string{path}, variant)
	if err != nil {
		return catalog.ResolvedImageURL{}, err
	}
	return resolved[path], nil
}

func (s *fakeExpiringImageSource) ResolveImageURLs(ctx context.Context, paths []string, variant string) (map[string]string, error) {
	resolved, err := s.ResolveImageURLsWithExpiry(ctx, paths, variant)
	if err != nil {
		return nil, err
	}
	urls := make(map[string]string, len(resolved))
	for path, value := range resolved {
		urls[path] = value.URL
	}
	return urls, nil
}

func (s *fakeExpiringImageSource) ResolveImageURLsWithExpiry(ctx context.Context, paths []string, variant string) (map[string]catalog.ResolvedImageURL, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.delay):
		}
	}
	resolved := make(map[string]catalog.ResolvedImageURL, len(paths))
	for _, path := range paths {
		resolved[path] = catalog.ResolvedImageURL{
			URL:       "plugin:" + variant + ":" + path,
			ExpiresAt: s.expiresAt,
		}
	}
	return resolved, nil
}

type scriptedImageSource struct {
	urls  map[string]string
	err   error
	calls atomic.Int32
}

func (s *scriptedImageSource) ResolveImageURL(ctx context.Context, path string, variant string) (string, error) {
	resolved, err := s.ResolveImageURLsWithExpiry(ctx, []string{path}, variant)
	if err != nil {
		return "", err
	}
	return resolved[path].URL, nil
}

func (s *scriptedImageSource) ResolveImageURLs(ctx context.Context, paths []string, variant string) (map[string]string, error) {
	resolved, err := s.ResolveImageURLsWithExpiry(ctx, paths, variant)
	if err != nil {
		return nil, err
	}
	urls := make(map[string]string, len(resolved))
	for path, value := range resolved {
		urls[path] = value.URL
	}
	return urls, nil
}

func (s *scriptedImageSource) ResolveImageURLsWithExpiry(_ context.Context, paths []string, variant string) (map[string]catalog.ResolvedImageURL, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	resolved := make(map[string]catalog.ResolvedImageURL, len(paths))
	for _, path := range paths {
		if url, ok := s.urls[path]; ok {
			resolved[path] = catalog.ResolvedImageURL{URL: url + ":" + variant}
		}
	}
	return resolved, nil
}

func TestPluginImageResolverCachesOnlyKnownUsableExpiries(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	source := &fakeExpiringImageSource{expiresAt: &expiresAt}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.RegisterSource("plug", source)

	for range 2 {
		got := resolver.ResolveImageURLWithExpiry(context.Background(), "plug://poster.jpg", "featured")
		if got.URL != "plugin:featured:poster.jpg" {
			t.Fatalf("resolved URL = %q", got.URL)
		}
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("plugin calls with usable expiry = %d, want 1", calls)
	}

	noExpirySource := &fakeExpiringImageSource{}
	noExpiryResolver := NewPluginImageResolver()
	defer noExpiryResolver.Close()
	noExpiryResolver.RegisterSource("plug", noExpirySource)
	for range 2 {
		_ = noExpiryResolver.ResolveImageURLWithExpiry(context.Background(), "plug://poster.jpg", "featured")
	}
	if calls := noExpirySource.calls.Load(); calls != 2 {
		t.Fatalf("plugin calls without expiry = %d, want 2", calls)
	}

	nearExpiry := time.Now().Add(time.Minute)
	nearExpirySource := &fakeExpiringImageSource{expiresAt: &nearExpiry}
	nearExpiryResolver := NewPluginImageResolver()
	defer nearExpiryResolver.Close()
	nearExpiryResolver.RegisterSource("plug", nearExpirySource)
	for range 2 {
		_ = nearExpiryResolver.ResolveImageURLWithExpiry(context.Background(), "plug://poster.jpg", "featured")
	}
	if calls := nearExpirySource.calls.Load(); calls != 2 {
		t.Fatalf("plugin calls with near expiry = %d, want 2", calls)
	}
}

func TestPluginImageResolverExplicitSourcesPrecedeLegacyFallbacks(t *testing.T) {
	resolver := NewPluginImageResolver()
	defer resolver.Close()

	explicit := &scriptedImageSource{urls: map[string]string{"poster.jpg": "explicit"}}
	legacy := &scriptedImageSource{urls: map[string]string{"poster.jpg": "legacy"}}
	resolver.ReplaceSources([]PluginImageResolverSourceRegistration{
		{
			Scheme:         "tmdb",
			Source:         legacy,
			Kind:           PluginImageResolverSourceLegacy,
			Priority:       1000,
			InstallationID: 1,
			CapabilityID:   "tmdb",
		},
		{
			Scheme:         "tmdb",
			Source:         explicit,
			Kind:           PluginImageResolverSourceExplicit,
			Priority:       0,
			InstallationID: 2,
			CapabilityID:   "tmdb",
		},
	})

	got := resolver.ResolveImageURL(context.Background(), "tmdb://poster.jpg", "card")
	if got != "explicit:card" {
		t.Fatalf("resolved URL = %q, want explicit source", got)
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("legacy source calls = %d, want 0 when explicit resolves", calls)
	}
}

func TestPluginImageResolverOrdersSourcesByPriority(t *testing.T) {
	resolver := NewPluginImageResolver()
	defer resolver.Close()

	low := &scriptedImageSource{urls: map[string]string{"poster.jpg": "low"}}
	high := &scriptedImageSource{urls: map[string]string{"poster.jpg": "high"}}
	resolver.ReplaceSources([]PluginImageResolverSourceRegistration{
		{
			Scheme:         "tmdb",
			Source:         low,
			Kind:           PluginImageResolverSourceExplicit,
			Priority:       10,
			InstallationID: 1,
			CapabilityID:   "low",
		},
		{
			Scheme:         "tmdb",
			Source:         high,
			Kind:           PluginImageResolverSourceExplicit,
			Priority:       50,
			InstallationID: 2,
			CapabilityID:   "high",
		},
	})

	got := resolver.ResolveImageURL(context.Background(), "tmdb://poster.jpg", "card")
	if got != "high:card" {
		t.Fatalf("resolved URL = %q, want high-priority source", got)
	}
}

func TestPluginImageResolverSkipsUnimplementedSourcesInEitherRegistrationOrder(t *testing.T) {
	cases := []struct {
		name          string
		registrations func(broken, working *scriptedImageSource) []PluginImageResolverSourceRegistration
	}{
		{
			name: "working registered first",
			registrations: func(broken, working *scriptedImageSource) []PluginImageResolverSourceRegistration {
				return []PluginImageResolverSourceRegistration{
					{Scheme: "tmdb", Source: working, Kind: PluginImageResolverSourceExplicit, Priority: 10, InstallationID: 2, CapabilityID: "working"},
					{Scheme: "tmdb", Source: broken, Kind: PluginImageResolverSourceExplicit, Priority: 20, InstallationID: 1, CapabilityID: "broken"},
				}
			},
		},
		{
			name: "broken registered first",
			registrations: func(broken, working *scriptedImageSource) []PluginImageResolverSourceRegistration {
				return []PluginImageResolverSourceRegistration{
					{Scheme: "tmdb", Source: broken, Kind: PluginImageResolverSourceExplicit, Priority: 20, InstallationID: 1, CapabilityID: "broken"},
					{Scheme: "tmdb", Source: working, Kind: PluginImageResolverSourceExplicit, Priority: 10, InstallationID: 2, CapabilityID: "working"},
				}
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewPluginImageResolver()
			defer resolver.Close()
			broken := &scriptedImageSource{err: status.Error(codes.Unimplemented, "method ResolveImageURLs not implemented")}
			working := &scriptedImageSource{urls: map[string]string{"poster.jpg": "working"}}
			resolver.ReplaceSources(tt.registrations(broken, working))

			got := resolver.ResolveImageURL(context.Background(), "tmdb://poster.jpg", "card")
			if got != "working:card" {
				t.Fatalf("resolved URL = %q, want fallback working source", got)
			}
		})
	}
}

func TestPluginImageResolverFallsThroughForPartialBatchResults(t *testing.T) {
	resolver := NewPluginImageResolver()
	defer resolver.Close()

	primary := &scriptedImageSource{urls: map[string]string{"a.jpg": "primary-a"}}
	secondary := &scriptedImageSource{urls: map[string]string{
		"a.jpg": "secondary-a",
		"b.jpg": "secondary-b",
	}}
	resolver.ReplaceSources([]PluginImageResolverSourceRegistration{
		{Scheme: "tmdb", Source: primary, Kind: PluginImageResolverSourceExplicit, Priority: 100, InstallationID: 1, CapabilityID: "primary"},
		{Scheme: "tmdb", Source: secondary, Kind: PluginImageResolverSourceExplicit, Priority: 10, InstallationID: 2, CapabilityID: "secondary"},
	})

	got := resolver.ResolveImageURLs(context.Background(), []string{"tmdb://a.jpg", "tmdb://b.jpg"}, "card")
	if got["tmdb://a.jpg"] != "primary-a:card" {
		t.Fatalf("a.jpg = %q, want primary result", got["tmdb://a.jpg"])
	}
	if got["tmdb://b.jpg"] != "secondary-b:card" {
		t.Fatalf("b.jpg = %q, want secondary fallback result", got["tmdb://b.jpg"])
	}
}

func TestPluginImageResolverDoesNotCacheEmptyFailure(t *testing.T) {
	resolver := NewPluginImageResolver()
	defer resolver.Close()

	source := &scriptedImageSource{err: status.Error(codes.Unavailable, "temporary outage")}
	resolver.ReplaceSources([]PluginImageResolverSourceRegistration{
		{Scheme: "tmdb", Source: source, Kind: PluginImageResolverSourceExplicit, Priority: 100, InstallationID: 1, CapabilityID: "tmdb"},
	})

	if got := resolver.ResolveImageURL(context.Background(), "tmdb://poster.jpg", "card"); got != "" {
		t.Fatalf("first resolved URL = %q, want empty during failure", got)
	}
	source.err = nil
	source.urls = map[string]string{"poster.jpg": "recovered"}

	got := resolver.ResolveImageURL(context.Background(), "tmdb://poster.jpg", "card")
	if got != "recovered:card" {
		t.Fatalf("second resolved URL = %q, want recovered result", got)
	}
	if calls := source.calls.Load(); calls != 2 {
		t.Fatalf("source calls = %d, want 2 to prove failure was not cached", calls)
	}
}

func TestPluginImageResolverCoalescesConcurrentBatchMisses(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	source := &fakeExpiringImageSource{expiresAt: &expiresAt, delay: 50 * time.Millisecond}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.RegisterSource("plug", source)

	const workers = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(i int) {
			defer wg.Done()
			<-start
			resolved := resolver.ResolveImageURLsWithExpiry(context.Background(), []string{"plug://a.jpg", "plug://b.jpg"}, "featured")
			if got := resolved["plug://a.jpg"].URL; got != "plugin:featured:a.jpg" {
				t.Errorf("worker %d resolved a.jpg = %q", i, got)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("plugin calls = %d, want 1", calls)
	}
}

// fakeArtworkURLResolver stands in for the canonical artwork store's URL
// minter. It counts batches so the caching assertions can tell a cache hit
// from a re-mint.
type fakeArtworkURLResolver struct {
	calls int
	ttl   time.Duration
}

func (r *fakeArtworkURLResolver) ResolveArtworkURLs(_ context.Context, keys []string) map[string]artworkstore.ResolvedURL {
	r.calls++
	expiresAt := time.Now().Add(r.ttl)
	resolved := make(map[string]artworkstore.ResolvedURL, len(keys))
	for _, key := range keys {
		resolved[key] = artworkstore.ResolvedURL{
			URL:       fmt.Sprintf("https://artwork.test/%s?mint=%d", key, r.calls),
			ExpiresAt: &expiresAt,
		}
	}
	return resolved
}

type blockingArtworkURLResolver struct {
	url     string
	ttl     time.Duration
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (r *blockingArtworkURLResolver) ResolveArtworkURLs(_ context.Context, keys []string) map[string]artworkstore.ResolvedURL {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	<-r.release
	expiresAt := time.Now().Add(r.ttl)
	resolved := make(map[string]artworkstore.ResolvedURL, len(keys))
	for _, key := range keys {
		resolved[key] = artworkstore.ResolvedURL{URL: r.url, ExpiresAt: &expiresAt}
	}
	return resolved
}

func TestPluginImageResolverStoredArtworkURLsCarryExpiry(t *testing.T) {
	artwork := &fakeArtworkURLResolver{ttl: 10 * time.Minute}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetArtworkURLResolver(artwork)

	const key = "artwork/v1/objects/poster/ab/abcd/original.webp"
	before := time.Now()
	first := resolver.ResolveImageURLWithExpiry(context.Background(), key, "featured")
	second := resolver.ResolveImageURLWithExpiry(context.Background(), key, "featured")

	if first.URL == "" || second.URL != first.URL {
		t.Fatalf("cached artwork URLs = first %q second %q", first.URL, second.URL)
	}
	if artwork.calls != 1 {
		t.Fatalf("artwork resolutions = %d, want 1", artwork.calls)
	}
	if first.ExpiresAt == nil {
		t.Fatal("resolved artwork URL missing expiry")
	}
	if first.ExpiresAt.Before(before.Add(9*time.Minute)) || first.ExpiresAt.After(before.Add(11*time.Minute)) {
		t.Fatalf("artwork expiry = %s, want about 10m from now", first.ExpiresAt.Sub(before))
	}
}

func TestPluginImageResolverDoesNotCacheInFlightResultAfterArtworkResolverSwap(t *testing.T) {
	old := &blockingArtworkURLResolver{
		url: "https://old.example/artwork", ttl: time.Hour,
		started: make(chan struct{}), release: make(chan struct{}),
	}
	current := &fakeArtworkURLResolver{ttl: time.Hour}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetArtworkURLResolver(old)

	const key = "artwork/v1/objects/poster/ab/abcd/original.webp"
	firstResult := make(chan catalog.ResolvedImageURL, 1)
	go func() {
		firstResult <- resolver.ResolveImageURLWithExpiry(context.Background(), key, "featured")
	}()
	<-old.started
	resolver.SetArtworkURLResolver(current)
	close(old.release)
	if got := <-firstResult; got.URL != old.url {
		t.Fatalf("in-flight caller URL = %q, want old resolver result %q", got.URL, old.url)
	}

	second := resolver.ResolveImageURLWithExpiry(context.Background(), key, "featured")
	third := resolver.ResolveImageURLWithExpiry(context.Background(), key, "featured")
	if second.URL == old.url || third.URL != second.URL {
		t.Fatalf("post-swap URLs = second %q third %q", second.URL, third.URL)
	}
	if current.calls != 1 {
		t.Fatalf("new resolver calls = %d, want one resolution followed by a cache hit", current.calls)
	}
	if old.calls.Load() != 1 {
		t.Fatalf("old resolver calls = %d, want one", old.calls.Load())
	}
}

func TestPluginImageResolverRoutesDirectLibraryIdentityThroughArtworkService(t *testing.T) {
	artwork := &fakeArtworkURLResolver{ttl: 10 * time.Minute}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetArtworkURLResolver(artwork)

	const reference = "library-artwork://opaque_unsigned_identity"
	resolved := resolver.ResolveImageURLWithExpiry(context.Background(), reference, "original")
	if resolved.URL == "" {
		t.Fatal("direct-library reference did not resolve")
	}
	if artwork.calls != 1 {
		t.Fatalf("artwork resolver calls = %d, want 1", artwork.calls)
	}
}

// Without a store URL minter a bare key is not a URL, and inventing one would
// publish a reference no client can fetch.
func TestPluginImageResolverWithoutArtworkResolverReturnsNoURL(t *testing.T) {
	resolver := NewPluginImageResolver()
	defer resolver.Close()

	resolved := resolver.ResolveImageURLWithExpiry(
		context.Background(),
		"artwork/v1/objects/poster/ab/abcd/original.webp",
		"featured",
	)
	if resolved.URL != "" {
		t.Fatalf("resolved URL = %q, want empty", resolved.URL)
	}
}

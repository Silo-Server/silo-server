package metadata

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

type fakeS3ImagePresigner struct {
	calls       int
	ttl         time.Duration
	keys        []string
	existing    map[string]bool
	existsErr   error
	existsCalls int
}

func (p *fakeS3ImagePresigner) PresignGetURL(_ context.Context, _ string, key string, expiry time.Duration) (string, error) {
	p.calls++
	p.ttl = expiry
	p.keys = append(p.keys, key)
	return fmt.Sprintf("s3:%s:%d", key, p.calls), nil
}

func (p *fakeS3ImagePresigner) Bucket() string {
	return "metadata"
}

func (p *fakeS3ImagePresigner) ObjectExists(_ context.Context, _ string, key string) (bool, error) {
	p.existsCalls++
	if p.existsErr != nil {
		return false, p.existsErr
	}
	return p.existing[key], nil
}

func TestPluginImageResolverS3URLsCarryConfiguredExpiry(t *testing.T) {
	presigner := &fakeS3ImagePresigner{}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetS3Presigner(presigner, 10*time.Minute)

	before := time.Now()
	first := resolver.ResolveImageURLWithExpiry(context.Background(), "poster.jpg", "featured")
	second := resolver.ResolveImageURLWithExpiry(context.Background(), "poster.jpg", "featured")

	if first.URL == "" || second.URL != first.URL {
		t.Fatalf("cached S3 URLs = first %q second %q", first.URL, second.URL)
	}
	if presigner.calls != 1 {
		t.Fatalf("s3 presign calls = %d, want 1", presigner.calls)
	}
	if presigner.ttl != 10*time.Minute {
		t.Fatalf("s3 presign ttl = %s, want 10m", presigner.ttl)
	}
	if first.ExpiresAt == nil {
		t.Fatal("S3 resolved URL missing expiry")
	}
	if first.ExpiresAt.Before(before.Add(9*time.Minute)) || first.ExpiresAt.After(before.Add(11*time.Minute)) {
		t.Fatalf("S3 expiry = %s, want about 10m from now", first.ExpiresAt.Sub(before))
	}
}

func TestPluginImageResolverS3FallsBackForMissingNewVariant(t *testing.T) {
	presigner := &fakeS3ImagePresigner{}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetS3Presigner(presigner, 10*time.Minute)

	got := resolver.ResolveImageURL(context.Background(), "tvdb/series/1/seasons/1/episodes/1/still/w780.rev123.webp", "featured")
	want := "s3:tvdb/series/1/seasons/1/episodes/1/still/w500.rev123.webp:1"
	if got != want {
		t.Fatalf("resolved URL = %q, want %q", got, want)
	}
	if presigner.existsCalls != 1 {
		t.Fatalf("ObjectExists calls = %d, want 1", presigner.existsCalls)
	}
	if len(presigner.keys) != 1 || presigner.keys[0] != "tvdb/series/1/seasons/1/episodes/1/still/w500.rev123.webp" {
		t.Fatalf("presigned keys = %v, want w500 fallback", presigner.keys)
	}
}

func TestPluginImageResolverS3PresignsExistingNewVariant(t *testing.T) {
	const key = "tvdb/series/1/seasons/1/episodes/1/still/w780.webp"
	presigner := &fakeS3ImagePresigner{existing: map[string]bool{key: true}}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetS3Presigner(presigner, 10*time.Minute)

	got := resolver.ResolveImageURL(context.Background(), key, "featured")
	want := "s3:tvdb/series/1/seasons/1/episodes/1/still/w780.webp:1"
	if got != want {
		t.Fatalf("resolved URL = %q, want %q", got, want)
	}
	if presigner.existsCalls != 1 {
		t.Fatalf("ObjectExists calls = %d, want 1", presigner.existsCalls)
	}
	if len(presigner.keys) != 1 || presigner.keys[0] != key {
		t.Fatalf("presigned keys = %v, want direct w780 key", presigner.keys)
	}

	resolver.urlCache.InvalidatePrefix("")
	got = resolver.ResolveImageURL(context.Background(), key, "featured")
	want = "s3:tvdb/series/1/seasons/1/episodes/1/still/w780.webp:2"
	if got != want {
		t.Fatalf("resolved URL after URL cache clear = %q, want %q", got, want)
	}
	if presigner.existsCalls != 1 {
		t.Fatalf("ObjectExists calls after cached existence = %d, want 1", presigner.existsCalls)
	}
}

func TestPluginImageResolverS3PresignsRequestedVariantWhenExistsCheckErrors(t *testing.T) {
	const key = "tvdb/series/1/seasons/1/episodes/1/still/w780.webp"
	presigner := &fakeS3ImagePresigner{existsErr: errors.New("s3 unavailable")}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetS3Presigner(presigner, 10*time.Minute)

	got := resolver.ResolveImageURL(context.Background(), key, "featured")
	want := "s3:tvdb/series/1/seasons/1/episodes/1/still/w780.webp:1"
	if got != want {
		t.Fatalf("resolved URL = %q, want %q", got, want)
	}
	if presigner.existsCalls != 1 {
		t.Fatalf("ObjectExists calls = %d, want 1", presigner.existsCalls)
	}
	if len(presigner.keys) != 1 || presigner.keys[0] != key {
		t.Fatalf("presigned keys = %v, want direct w780 key", presigner.keys)
	}

	resolver.urlCache.InvalidatePrefix("")
	got = resolver.ResolveImageURL(context.Background(), key, "featured")
	want = "s3:tvdb/series/1/seasons/1/episodes/1/still/w780.webp:2"
	if got != want {
		t.Fatalf("resolved URL after URL cache clear = %q, want %q", got, want)
	}
	if presigner.existsCalls != 2 {
		t.Fatalf("ObjectExists calls after URL cache clear = %d, want 2 to prove error was not cached", presigner.existsCalls)
	}
}

func TestPluginImageResolverS3FallbackURLCacheUsesMissingVariantTTL(t *testing.T) {
	presigner := &fakeS3ImagePresigner{}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetS3Presigner(presigner, 4*time.Hour)

	resolved := resolver.ResolveImageURLWithExpiry(context.Background(), "tvdb/series/1/seasons/1/episodes/1/still/w780.rev123.webp", "featured")
	if resolved.URL != "s3:tvdb/series/1/seasons/1/episodes/1/still/w500.rev123.webp:1" {
		t.Fatalf("resolved URL = %q, want w500 fallback", resolved.URL)
	}
	if resolved.ExpiresAt == nil {
		t.Fatal("fallback resolved URL missing expiry")
	}
	cacheTTL := cacheTTLForResolvedURL(resolved, time.Now())
	if cacheTTL < missingVariantCacheTTL-time.Second || cacheTTL > missingVariantCacheTTL+time.Second {
		t.Fatalf("fallback URL cache TTL = %s, want about %s", cacheTTL, missingVariantCacheTTL)
	}

	cached := resolver.ResolveImageURLWithExpiry(context.Background(), "tvdb/series/1/seasons/1/episodes/1/still/w780.rev123.webp", "featured")
	if cached.URL != resolved.URL {
		t.Fatalf("cached fallback URL = %q, want %q", cached.URL, resolved.URL)
	}
	if presigner.calls != 1 {
		t.Fatalf("s3 presign calls = %d, want cached fallback URL", presigner.calls)
	}
}

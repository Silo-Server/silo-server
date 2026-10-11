package metadata

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
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

type fakeStoredArtworkResolver struct {
	calls int
	ttl   time.Duration
}

func (r *fakeStoredArtworkResolver) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		r.calls++
		expiry := time.Now().Add(r.ttl)
		out[key] = catalog.ResolvedImageURL{URL: fmt.Sprintf("stored:%s:%d", key, r.calls), ExpiresAt: &expiry}
	}
	return out
}

func TestPluginImageResolverStoredURLsCarryResolverExpiry(t *testing.T) {
	stored := &fakeStoredArtworkResolver{ttl: 10 * time.Minute}
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.SetArtworkResolver(stored)

	before := time.Now()
	first := resolver.ResolveImageURLWithExpiry(context.Background(), "poster.jpg", "featured")
	second := resolver.ResolveImageURLWithExpiry(context.Background(), "poster.jpg", "featured")

	if first.URL == "" || second.URL != first.URL {
		t.Fatalf("cached stored URLs = first %q second %q", first.URL, second.URL)
	}
	if stored.calls != 1 {
		t.Fatalf("stored resolver calls = %d, want 1", stored.calls)
	}
	if first.ExpiresAt == nil {
		t.Fatal("stored resolved URL missing expiry")
	}
	if first.ExpiresAt.Before(before.Add(4*time.Minute)) || first.ExpiresAt.After(before.Add(11*time.Minute)) {
		t.Fatalf("stored expiry = %s, want between the cache bound and the resolver TTL", first.ExpiresAt.Sub(before))
	}
	if resolver.ResolveImageURL(context.Background(), "unstored.jpg", "featured") == "" {
		t.Fatal("stored key without an availability reader must resolve to itself")
	}
}

// blockingImageSource answers once released and fails, as a gRPC plugin does,
// when its context ends first.
type blockingImageSource struct {
	release chan struct{}
	calls   atomic.Int32
}

func (s *blockingImageSource) ResolveImageURL(ctx context.Context, path string, variant string) (string, error) {
	resolved, err := s.ResolveImageURLs(ctx, []string{path}, variant)
	return resolved[path], err
}

func (s *blockingImageSource) ResolveImageURLs(ctx context.Context, paths []string, _ string) (map[string]string, error) {
	s.calls.Add(1)
	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-s.release:
	}
	resolved := make(map[string]string, len(paths))
	for _, path := range paths {
		resolved[path] = "resolved:" + path
	}
	return resolved, nil
}

// Callers asking for the same batch share one plugin call. The request that
// started it leaving must not empty the batch for the others.
func TestPluginImageResolverFollowerSurvivesLeaderCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &blockingImageSource{release: make(chan struct{})}
		resolver := NewPluginImageResolver()
		defer resolver.Close()
		resolver.RegisterSource("plug", source)
		paths := []string{"plug://a.jpg", "plug://b.jpg"}

		leaderCtx, cancelLeader := context.WithCancel(t.Context())
		leaderDone := make(chan struct{})
		go func() {
			defer close(leaderDone)
			resolver.ResolveImageURLsWithExpiry(leaderCtx, paths, "card")
		}()
		synctest.Wait() // the leader's plugin call is in flight
		var followed map[string]catalog.ResolvedImageURL
		followerDone := make(chan struct{})
		go func() {
			defer close(followerDone)
			followed = resolver.ResolveImageURLsWithExpiry(t.Context(), paths, "card")
		}()
		synctest.Wait() // the follower is waiting on the same call
		cancelLeader()
		<-leaderDone
		close(source.release)
		<-followerDone

		for _, path := range paths {
			if got := followed[path].URL; got != "resolved:"+path[len("plug://"):] {
				t.Errorf("follower resolved %s = %q after the leader left", path, got)
			}
		}
		if calls := source.calls.Load(); calls != 1 {
			t.Fatalf("plugin calls = %d, want 1 shared call", calls)
		}
	})
}

// stubPluginImageClient answers like a running plugin behind the plugin host:
// a call without a deadline gets the plugin default, as pluginhost gives it,
// and fails as gRPC does when its deadline passes before the answer.
type stubPluginImageClient struct {
	answerAfter time.Duration // zero never answers
	budget      atomic.Int64  // time the last call had to answer
}

func (c *stubPluginImageClient) ResolveImageURL(ctx context.Context, req *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	resp, err := c.ResolveImageURLs(ctx, &pluginv1.ResolveImageURLsRequest{Paths: []string{req.GetPath()}, Variant: req.GetVariant()})
	if err != nil {
		return nil, err
	}
	return &pluginv1.ResolveImageURLResponse{Url: resp.GetUrls()[req.GetPath()]}, nil
}

func (c *stubPluginImageClient) ResolveImageURLs(ctx context.Context, req *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pluginhost.DefaultMetadataTimeout)
		defer cancel()
	}
	deadline, _ := ctx.Deadline()
	c.budget.Store(int64(time.Until(deadline)))
	var answered <-chan time.Time
	if c.answerAfter > 0 {
		answered = time.After(c.answerAfter)
	}
	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-answered:
	}
	urls := make(map[string]string, len(req.GetPaths()))
	for _, path := range req.GetPaths() {
		urls[path] = "plugin:" + path
	}
	return &pluginv1.ResolveImageURLsResponse{Urls: urls}, nil
}

// stubPluginSource reaches client after a launch of the given length, as a
// plugin that is not running yet does. The plugin service runs the launch
// detached from the caller, so no context can cut it short.
func stubPluginSource(launch time.Duration, client *stubPluginImageClient) PluginImageResolverSource {
	return NewPluginClientSource(1, "tmdb", func(context.Context, int, string) (PluginMetadataClient, error) {
		time.Sleep(launch)
		return client, nil
	})
}

// A hung source runs out of the plugin's own time and leaves the fallback a
// full budget of its own, whatever deadline the caller has.
func TestPluginImageResolverFallsBackAfterAHungSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		legacy := &scriptedImageSource{urls: map[string]string{"poster.jpg": "legacy"}}
		resolver := NewPluginImageResolver()
		defer resolver.Close()
		resolver.ReplaceSources([]PluginImageResolverSourceRegistration{
			{Scheme: "tmdb", Source: stubPluginSource(0, &stubPluginImageClient{}), Kind: PluginImageResolverSourceExplicit, Priority: 100, InstallationID: 1, CapabilityID: "tmdb"},
			{Scheme: "tmdb", Source: legacy, Kind: PluginImageResolverSourceLegacy, Priority: 100, InstallationID: 2, CapabilityID: "tmdb"},
		})
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		start := time.Now()
		resolved := resolver.ResolveImageURLsWithExpiry(ctx, []string{"tmdb://poster.jpg"}, "card")
		if got := resolved["tmdb://poster.jpg"].URL; got != "legacy:card" {
			t.Fatalf("resolved URL = %q, want the legacy fallback", got)
		}
		if elapsed := time.Since(start); elapsed != pluginhost.DefaultMetadataTimeout {
			t.Fatalf("fallback answered after %v, want the plugin default %v", elapsed, pluginhost.DefaultMetadataTimeout)
		}
	})
}

// A plugin that is not running yet launches before the call. The call after a
// slow but healthy launch still gets the plugin's full default to answer.
func TestPluginImageResolverColdStartKeepsTheCallBudget(t *testing.T) {
	for _, launch := range []time.Duration{25 * time.Second, 40 * time.Second} {
		t.Run(launch.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := &stubPluginImageClient{answerAfter: 10 * time.Second}
				resolver := NewPluginImageResolver()
				defer resolver.Close()
				resolver.ReplaceSources([]PluginImageResolverSourceRegistration{
					{Scheme: "tmdb", Source: stubPluginSource(launch, client), Kind: PluginImageResolverSourceExplicit, Priority: 100, InstallationID: 1, CapabilityID: "tmdb"},
				})
				resolved := resolver.ResolveImageURLsWithExpiry(t.Context(), []string{"tmdb://poster.jpg"}, "card")
				if got := resolved["tmdb://poster.jpg"].URL; got != "plugin:poster.jpg" {
					t.Fatalf("resolved URL = %q after a %v launch, want the plugin's answer", got, launch)
				}
				if budget := time.Duration(client.budget.Load()); budget != pluginhost.DefaultMetadataTimeout {
					t.Fatalf("call had %v after a %v launch, want the plugin default %v", budget, launch, pluginhost.DefaultMetadataTimeout)
				}
			})
		})
	}
}

type panickingImageSource struct{}

func (panickingImageSource) ResolveImageURL(context.Context, string, string) (string, error) {
	panic("source bug")
}

func (panickingImageSource) ResolveImageURLs(context.Context, []string, string) (map[string]string, error) {
	panic("source bug")
}

// The shared call runs on its own goroutine, where a panic would end the
// process instead of reaching the request's recovery.
func TestPluginImageResolverSurvivesSourcePanic(t *testing.T) {
	resolver := NewPluginImageResolver()
	defer resolver.Close()
	resolver.RegisterSource("plug", panickingImageSource{})
	if got := resolver.ResolveImageURLsWithExpiry(t.Context(), []string{"plug://a.jpg"}, "card"); len(got) != 0 {
		t.Fatalf("resolved %v from a panicking source", got)
	}
}

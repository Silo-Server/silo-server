package metadata

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"

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

package streamtelemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// countingStore wraps LocalStore so a test can count how many times the cache
// actually rebuilt the view, and force a build failure.
type countingStore struct {
	mu     sync.Mutex
	inner  *LocalStore
	loads  int
	failed error
}

func newCountingStore() *countingStore { return &countingStore{inner: NewLocalStore()} }

func (s *countingStore) Publish(ctx context.Context, snapshot Snapshot) error {
	return s.inner.Publish(ctx, snapshot)
}
func (s *countingStore) Load(ctx context.Context) (Snapshot, error) { return s.inner.Load(ctx) }
func (s *countingStore) Leave(ctx context.Context) error            { return s.inner.Leave(ctx) }
func (s *countingStore) LoadAll(ctx context.Context) (PublisherSet, error) {
	s.mu.Lock()
	s.loads++
	failed := s.failed
	s.mu.Unlock()
	if failed != nil {
		return PublisherSet{}, failed
	}
	return s.inner.LoadAll(ctx)
}
func (s *countingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}
func (s *countingStore) fail(err error) {
	s.mu.Lock()
	s.failed = err
	s.mu.Unlock()
}

func newCacheFixture(t *testing.T, ttl time.Duration) (*ViewCache, *Registry, *countingStore) {
	t.Helper()
	store := newCountingStore()
	registry := NewRegistry(testConfig(), store, nil)
	t.Cleanup(func() { _ = registry.Stop(context.Background()) })
	// Publish one snapshot so the merged view has a publisher to report.
	if err := store.Publish(context.Background(), registry.Snapshot()); err != nil {
		t.Fatal(err)
	}
	return NewViewCache(registry, ttl, nil), registry, store
}

func TestViewCacheServesWithinTTLAndRebuildsAfter(t *testing.T) {
	cache, _, store := newCacheFixture(t, time.Minute)

	if _, status := cache.View(context.Background()); !status.Available || status.Stale {
		t.Fatalf("first read = %+v", status)
	}
	if store.count() != 1 {
		t.Fatalf("builds after first read = %d, want 1", store.count())
	}

	// Inside the TTL the cached value is served without touching the store.
	for i := 0; i < 5; i++ {
		if _, status := cache.View(context.Background()); status.Stale {
			t.Fatalf("read %d was stale inside the TTL: %+v", i, status)
		}
	}
	if store.count() != 1 {
		t.Fatalf("builds inside TTL = %d, want 1 — the cache rebuilt per read", store.count())
	}

	// Past the TTL a read rebuilds. Move the package clock rather than sleeping.
	restore := now
	now = func() time.Time { return restore().Add(2 * time.Minute) }
	t.Cleanup(func() { now = restore })
	if _, status := cache.View(context.Background()); !status.Available {
		t.Fatalf("read after TTL = %+v", status)
	}
	if store.count() != 2 {
		t.Fatalf("builds after TTL = %d, want 2", store.count())
	}
}

// Many admins refreshing at once must not stampede a 347 ms rebuild.
func TestViewCacheSingleFlights(t *testing.T) {
	cache, _, store := newCacheFixture(t, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, status := cache.View(context.Background()); !status.Available {
				t.Errorf("concurrent read had no view")
			}
		}()
	}
	wg.Wait()
	if got := store.count(); got != 1 {
		t.Fatalf("builds under 16 concurrent readers = %d, want 1", got)
	}
}

// A failed refresh must keep the last good view rather than going blind: a
// consumer that saw an empty view would read it as "nothing is streaming".
func TestViewCacheKeepsLastGoodViewOnFailure(t *testing.T) {
	cache, _, store := newCacheFixture(t, time.Minute)
	if _, status := cache.View(context.Background()); !status.Available {
		t.Fatal("first read failed")
	}

	store.fail(errors.New("redis is down"))
	restore := now
	now = func() time.Time { return restore().Add(2 * time.Minute) }
	t.Cleanup(func() { now = restore })

	view, status := cache.View(context.Background())
	if !status.Available {
		t.Fatal("a failed refresh discarded the last good view")
	}
	if !status.Stale {
		t.Fatalf("a failed refresh was not reported stale: %+v", status)
	}
	if status.LastError == "" || status.Failures != 1 {
		t.Fatalf("failure not reported: %+v", status)
	}
	if view.Epoch == "" && len(view.Publishers) == 0 {
		t.Fatalf("served view is empty: %+v", view)
	}
}

func TestViewCacheReportsUnavailableWithoutTelemetry(t *testing.T) {
	t.Run("nil cache", func(t *testing.T) {
		var cache *ViewCache
		if _, status := cache.View(context.Background()); status.Available {
			t.Fatalf("nil cache reported available: %+v", status)
		}
	})
	t.Run("disabled registry", func(t *testing.T) {
		cfg := testConfig()
		cfg.Enabled = false
		registry := NewRegistry(cfg, NewLocalStore(), nil)
		t.Cleanup(func() { _ = registry.Stop(context.Background()) })
		cache := NewViewCache(registry, time.Minute, nil)
		if _, status := cache.View(context.Background()); status.Available {
			t.Fatalf("disabled registry reported available: %+v", status)
		}
	})
	t.Run("nil registry", func(t *testing.T) {
		cache := NewViewCache(nil, time.Minute, nil)
		if _, status := cache.View(context.Background()); status.Available {
			t.Fatalf("nil registry reported available: %+v", status)
		}
	})
}

func TestViewCacheDefaultsTTL(t *testing.T) {
	if got := NewViewCache(nil, 0, nil).TTL(); got != DefaultViewTTL {
		t.Fatalf("TTL = %s, want %s", got, DefaultViewTTL)
	}
	if got := NewViewCache(nil, -time.Second, nil).TTL(); got != DefaultViewTTL {
		t.Fatalf("negative TTL = %s, want %s", got, DefaultViewTTL)
	}
}

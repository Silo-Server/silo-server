package ebookconvert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestCache(t *testing.T, opts CacheOptions) *Cache {
	t.Helper()
	conv := newTestConverter(t)
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}
	c, err := NewCache(conv, opts)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return c
}

func keyFor(t *testing.T, fileID int, path string) SourceKey {
	t.Helper()
	k, err := SourceKeyFromStat(fileID, path)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCache_MissThenHit(t *testing.T) {
	c := newTestCache(t, CacheOptions{})
	src := filepath.Join("testdata", "sample-ncx.mobi")
	key := keyFor(t, 1, src)

	p1, err := c.GetOrConvert(context.Background(), src, key)
	if err != nil {
		t.Fatalf("first GetOrConvert: %v", err)
	}
	assertValidEpub(t, p1)
	fi1, _ := os.Stat(p1)

	p2, err := c.GetOrConvert(context.Background(), src, key)
	if err != nil {
		t.Fatalf("second GetOrConvert: %v", err)
	}
	if p1 != p2 {
		t.Fatalf("hit returned different path: %s vs %s", p1, p2)
	}
	fi2, _ := os.Stat(p2)
	// A hit refreshes mtime for LRU but must NOT reconvert: the cached entry is
	// the same underlying file, not a freshly converted+renamed replacement.
	if !os.SameFile(fi1, fi2) {
		t.Fatal("cache hit reconverted the file (replaced the cached entry)")
	}
}

func TestCache_KeyChangesOnModTime(t *testing.T) {
	c := newTestCache(t, CacheOptions{})
	src := filepath.Join("testdata", "sample-ncx.mobi")
	k1 := keyFor(t, 1, src)
	k2 := k1
	k2.ModTimeNano = k1.ModTimeNano + 1 // simulate a re-scanned/replaced file
	if k1.hash() == k2.hash() {
		t.Fatal("expected different cache keys for different mtime")
	}
	p1, err := c.GetOrConvert(context.Background(), src, k1)
	if err != nil {
		t.Fatalf("convert k1: %v", err)
	}
	p2, err := c.GetOrConvert(context.Background(), src, k2)
	if err != nil {
		t.Fatalf("convert k2: %v", err)
	}
	if p1 == p2 {
		t.Fatal("different keys must map to different cache files")
	}
}

// A transient timeout is NOT negatively cached: it would otherwise wedge a
// slow-but-convertible book onto the raw-fallback path for the whole TTL.
func TestCache_TimeoutNotNegativelyCached(t *testing.T) {
	conv, err := NewConverter(context.Background(), Options{Timeout: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	t.Cleanup(func() { _ = conv.Close(context.Background()) })
	c, err := NewCache(conv, CacheOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	src := filepath.Join("testdata", "sample-ncx.mobi")
	key := keyFor(t, 1, src)

	_, err = c.GetOrConvert(context.Background(), src, key)
	if !errors.Is(err, ErrConversionTimedOut) {
		t.Fatalf("got %v, want ErrConversionTimedOut", err)
	}
	c.mu.Lock()
	_, cached := c.neg[key.hash()]
	c.mu.Unlock()
	if cached {
		t.Fatal("a transient timeout must not be negatively cached")
	}
}

// Eviction must never delete the EPUB it is about to hand back, even when the
// configured budget is smaller than a single converted file.
func TestCache_EvictionKeepsJustConverted(t *testing.T) {
	c := newTestCache(t, CacheOptions{Dir: t.TempDir(), MaxBytes: 1})
	src := filepath.Join("testdata", "sample-ncx.mobi")
	p, err := c.GetOrConvert(context.Background(), src, keyFor(t, 1, src))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if fi, statErr := os.Stat(p); statErr != nil || fi.Size() == 0 {
		t.Fatalf("returned path must exist and be non-empty (eviction deleted it): stat=%v", statErr)
	}
}

// Eviction must ignore other conversions' in-flight temp files; deleting one
// would corrupt a concurrent conversion mid-flight.
func TestCache_EvictionSkipsInFlightTemps(t *testing.T) {
	dir := t.TempDir()
	c := newTestCache(t, CacheOptions{Dir: dir, MaxBytes: 1})
	tmp := filepath.Join(dir, "converting-inflight.epub")
	if err := os.WriteFile(tmp, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour) // make the temp the "oldest" candidate
	_ = os.Chtimes(tmp, old, old)

	src := filepath.Join("testdata", "sample-ncx.mobi")
	if _, err := c.GetOrConvert(context.Background(), src, keyFor(t, 1, src)); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("eviction deleted an in-flight temp file: %v", err)
	}
}

// A cache hit refreshes mtime so the mtime-ordered budget eviction behaves as a
// real LRU (most-recently-read entries survive).
func TestCache_HitTouchesModTimeForLRU(t *testing.T) {
	c := newTestCache(t, CacheOptions{})
	src := filepath.Join("testdata", "sample-ncx.mobi")
	key := keyFor(t, 1, src)
	p, err := c.GetOrConvert(context.Background(), src, key)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p, old, old)

	if _, err := c.GetOrConvert(context.Background(), src, key); err != nil {
		t.Fatalf("hit: %v", err)
	}
	fi, _ := os.Stat(p)
	if time.Since(fi.ModTime()) > time.Minute {
		t.Fatalf("cache hit did not refresh mtime for LRU; mtime is %v old", time.Since(fi.ModTime()))
	}
}

// A single caller abandoning its request (cancellation) must not abort the
// shared conversion for everyone else: the detached work still completes and
// populates the cache.
func TestCache_CallerCancelDoesNotAbortSharedWork(t *testing.T) {
	c := newTestCache(t, CacheOptions{})
	src := filepath.Join("testdata", "sample-ncx.mobi")
	key := keyFor(t, 1, src)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller bails before the conversion can finish
	_, _ = c.GetOrConvert(ctx, src, key)

	dst := filepath.Join(c.opts.Dir, key.hash()+".epub")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
			return // shared work completed despite the caller canceling — good
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("detached conversion did not populate the cache after the caller canceled")
}

func TestCache_NegativeCachesDRM(t *testing.T) {
	c := newTestCache(t, CacheOptions{})
	src := filepath.Join("testdata", "sample-drm-v1.mobi")
	key := keyFor(t, 7, src)

	err1 := mustConvertErr(t, c, src, key)
	if !errors.Is(err1, ErrDRMProtected) {
		t.Fatalf("got %v, want ErrDRMProtected", err1)
	}
	// Second call should be served from the negative cache (still DRM error).
	err2 := mustConvertErr(t, c, src, key)
	if !errors.Is(err2, ErrDRMProtected) {
		t.Fatalf("negative-cached call: got %v, want ErrDRMProtected", err2)
	}
	c.mu.Lock()
	_, cached := c.neg[key.hash()]
	c.mu.Unlock()
	if !cached {
		t.Fatal("DRM result was not negatively cached")
	}
}

func TestCache_Singleflight(t *testing.T) {
	c := newTestCache(t, CacheOptions{})
	src := filepath.Join("testdata", "sample-ncx.mobi")
	key := keyFor(t, 3, src)

	const n = 8
	var wg sync.WaitGroup
	var firstPath atomic.Value
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := c.GetOrConvert(context.Background(), src, key)
			if err != nil {
				errs <- err
				return
			}
			firstPath.Store(p)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent GetOrConvert: %v", err)
	}
	if p, _ := firstPath.Load().(string); p == "" {
		t.Fatal("no path produced")
	}
}

func TestCache_BudgetEviction(t *testing.T) {
	dir := t.TempDir()
	// Budget fits one converted EPUB (~2.4 KiB) but not two, so converting a
	// second entry evicts the older one — while never evicting the entry just
	// handed back.
	const budget = 3000
	c := newTestCache(t, CacheOptions{Dir: dir, MaxBytes: budget})
	src := filepath.Join("testdata", "sample-ncx.mobi")

	p1, err := c.GetOrConvert(context.Background(), src, keyFor(t, 1, src))
	if err != nil {
		t.Fatalf("convert 1: %v", err)
	}
	// Force the first entry to be the oldest (the eviction victim).
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p1, old, old)

	k2 := keyFor(t, 2, src)
	k2.FileID = 2
	p2, err := c.GetOrConvert(context.Background(), src, k2)
	if err != nil {
		t.Fatalf("convert 2: %v", err)
	}

	if _, err := os.Stat(p2); err != nil {
		t.Fatalf("just-converted entry must survive eviction: %v", err)
	}
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Fatalf("older entry should have been evicted, stat err = %v", err)
	}

	epubs, _ := filepath.Glob(filepath.Join(dir, "*.epub"))
	var total int64
	for _, e := range epubs {
		fi, _ := os.Stat(e)
		total += fi.Size()
	}
	if total > budget {
		t.Fatalf("budget not enforced: total=%d > %d (%d files)", total, budget, len(epubs))
	}
}

func TestModuleVersion_Stable(t *testing.T) {
	if len(moduleVersion) != 16 {
		t.Fatalf("moduleVersion len = %d, want 16", len(moduleVersion))
	}
}

func mustConvertErr(t *testing.T, c *Cache, src string, key SourceKey) error {
	t.Helper()
	_, err := c.GetOrConvert(context.Background(), src, key)
	return err
}

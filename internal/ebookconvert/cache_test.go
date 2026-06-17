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
	// A hit must not rewrite the file (same mtime => not reconverted).
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("cache hit reconverted the file")
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
	p1, _ := c.GetOrConvert(context.Background(), src, k1)
	p2, _ := c.GetOrConvert(context.Background(), src, k2)
	if p1 == p2 {
		t.Fatal("different keys must map to different cache files")
	}
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
	// Tiny budget so any second entry forces eviction of the first.
	c := newTestCache(t, CacheOptions{Dir: dir, MaxBytes: 1500})
	src := filepath.Join("testdata", "sample-ncx.mobi")

	p1, err := c.GetOrConvert(context.Background(), src, keyFor(t, 1, src))
	if err != nil {
		t.Fatalf("convert 1: %v", err)
	}
	// Force the first entry to be the oldest.
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p1, old, old)

	k2 := keyFor(t, 2, src)
	k2.FileID = 2
	if _, err := c.GetOrConvert(context.Background(), src, k2); err != nil {
		t.Fatalf("convert 2: %v", err)
	}

	epubs, _ := filepath.Glob(filepath.Join(dir, "*.epub"))
	var total int64
	for _, e := range epubs {
		fi, _ := os.Stat(e)
		total += fi.Size()
	}
	if total > 1500 {
		t.Fatalf("budget not enforced: total=%d > 1500 (%d files)", total, len(epubs))
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

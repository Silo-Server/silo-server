package ebookconvert

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	t.Cleanup(func() { _ = c.root.Close() })
	return c
}

// convertPath runs GetOrConvert and returns the cache entry's path, closing the
// descriptor so the existing path-based assertions apply.
func convertPath(t *testing.T, c *Cache, src string, key SourceKey) (string, error) {
	t.Helper()
	f, err := c.GetOrConvert(t.Context(), src, key)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	return f.Name(), nil
}

func keyFor(t *testing.T, fileID int, path string) SourceKey {
	t.Helper()
	k, err := SourceKeyFromStat(fileID, path)
	if err != nil {
		t.Fatal(err)
	}
	return k
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
	if _, err := convertPath(t, c, src, keyFor(t, 1, src)); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("eviction deleted an in-flight temp file: %v", err)
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

	p1, err := convertPath(t, c, src, keyFor(t, 1, src))
	if err != nil {
		t.Fatalf("convert 1: %v", err)
	}
	// Force the first entry to be the oldest (the eviction victim).
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p1, old, old)

	k2 := keyFor(t, 2, src)
	k2.FileID = 2
	p2, err := convertPath(t, c, src, k2)
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

// The default cache dir sits at a predictable path in the shared temp dir.
// Another local account that creates it first must not get its entries served:
// NewCache refuses a root it could control, and a symlinked entry is never a
// hit, so a reader can never receive a file outside the cache.
func TestCache_RefusesEntriesOthersControl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on Unix modes and unprivileged symlinks")
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("PRIVATE-SERVICE-FILE"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("root writable by others", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "silo-ebook-epub")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil { // Chmod: the umask would clear Mkdir's bits
			t.Fatal(err)
		}
		_, err := NewCache(newTestConverter(t), CacheOptions{Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "writable by its group or other users") {
			t.Fatalf("NewCache error = %v, want a refusal of the writable root", err)
		}
	})
	t.Run("entry symlinked outside the cache", func(t *testing.T) {
		c := newTestCache(t, CacheOptions{})
		src := filepath.Join("testdata", "sample-ncx.mobi")
		key := keyFor(t, 1, src)
		entry := filepath.Join(c.opts.Dir, key.hash()+".epub")
		if err := os.Symlink(secret, entry); err != nil {
			t.Fatal(err)
		}

		if f, lookupErr, ok := c.Lookup(key); ok || lookupErr != nil {
			if f != nil {
				_ = f.Close()
			}
			t.Fatalf("Lookup = ok %v, err %v; want a miss", ok, lookupErr)
		}
		// A miss converts again and the rename replaces the symlink with the
		// real conversion.
		p, err := convertPath(t, c, src, key)
		if err != nil {
			t.Fatalf("GetOrConvert: %v", err)
		}
		assertValidEpub(t, p)
		if fi, err := os.Lstat(entry); err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("entry after conversion: %v, %v; want a regular file", fi, err)
		}
	})
	// A parent dir another account can write lets it rename the cache dir
	// away after NewCache verified it and put a symlink in its place.
	// Conversion and eviction must stay in the dir NewCache verified.
	t.Run("cache dir replaced after start", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "silo-ebook-epub")
		c := newTestCache(t, CacheOptions{Dir: dir, MaxBytes: 1})
		victim := t.TempDir()
		victimFile := filepath.Join(victim, "book.epub")
		if err := os.WriteFile(victimFile, []byte("library book"), 0o600); err != nil {
			t.Fatal(err)
		}
		moved := filepath.Join(parent, "moved")
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, dir); err != nil {
			t.Fatal(err)
		}

		src := filepath.Join("testdata", "sample-ncx.mobi")
		key := keyFor(t, 1, src)
		if _, err := convertPath(t, c, src, key); err != nil {
			t.Fatalf("GetOrConvert: %v", err)
		}
		if _, err := os.Stat(victimFile); err != nil {
			t.Fatalf("eviction reached the swapped-in dir: %v", err)
		}
		if names, _ := filepath.Glob(filepath.Join(victim, "*")); len(names) != 1 {
			t.Fatalf("swapped-in dir holds %v, want only book.epub", names)
		}
		assertValidEpub(t, filepath.Join(moved, key.hash()+".epub"))
	})
}

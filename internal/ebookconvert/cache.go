package ebookconvert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// moduleVersion is a short fingerprint of the embedded wasm, mixed into every
// cache key so bumping mobitool.wasm transparently invalidates old conversions.
var moduleVersion = func() string {
	sum := sha256.Sum256(mobitoolWasm)
	return hex.EncodeToString(sum[:])[:16]
}()

// ModuleVersion returns the embedded converter's fingerprint. Callers can mix
// it into HTTP ETags so a converter bump invalidates client/proxy caches.
func ModuleVersion() string { return moduleVersion }

// SourceKey identifies a source file for caching. Built by the caller from the
// catalog/file metadata. Size+ModTimeNano are cheap and computed on every read;
// Checksum (the scanner's content hash, if available) hardens against a replace
// that preserves size+mtime — set it when you have it.
type SourceKey struct {
	FileID      int
	Size        int64
	ModTimeNano int64
	Checksum    string // optional; scanner checksum
}

func (k SourceKey) hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|mod=%s|id=%d|sz=%d|mt=%d|ck=%s",
		moduleVersion, k.FileID, k.Size, k.ModTimeNano, k.Checksum)
	return hex.EncodeToString(h.Sum(nil))
}

// CacheKey returns the exact identity used to cache this source's conversion
// (source identity + converter module version). Use it to derive an HTTP ETag
// so the validator and the cache entry can never disagree.
func (k SourceKey) CacheKey() string { return k.hash() }

// CacheOptions configures a Cache.
type CacheOptions struct {
	// Dir is the on-disk cache root for converted EPUBs (required).
	Dir string
	// MaxBytes bounds total cached EPUB size; oldest are evicted past it.
	// Zero uses DefaultCacheMaxBytes.
	MaxBytes int64
	// NegativeTTL is how long a DRM/failed result is remembered to avoid
	// reconverting a known-bad source. Zero uses DefaultNegativeTTL.
	NegativeTTL time.Duration
}

const (
	DefaultCacheMaxBytes = 2 << 30 // 2 GiB
	DefaultNegativeTTL   = 6 * time.Hour
)

// Cache wraps a Converter with an on-disk, size-bounded, singleflighted cache
// and an in-memory negative cache for DRM/failed sources.
type Cache struct {
	conv *Converter
	opts CacheOptions

	group singleflight.Group

	mu  sync.Mutex
	neg map[string]negEntry // key hash -> negative result
}

type negEntry struct {
	err    error
	expiry time.Time
}

// NewCache creates the cache dir and returns a ready Cache.
func NewCache(conv *Converter, opts CacheOptions) (*Cache, error) {
	if conv == nil {
		return nil, ErrUnavailable
	}
	if opts.Dir == "" {
		return nil, errors.New("ebookconvert: cache Dir is required")
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultCacheMaxBytes
	}
	if opts.NegativeTTL <= 0 {
		opts.NegativeTTL = DefaultNegativeTTL
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("ebookconvert: create cache dir: %w", err)
	}
	return &Cache{conv: conv, opts: opts, neg: make(map[string]negEntry)}, nil
}

// GetOrConvert returns the path to a cached EPUB for srcPath/key, converting on
// miss. Concurrent calls for the same key collapse to a single conversion.
// Returns ErrDRMProtected / ErrConversionFailed (also served from the negative
// cache), ErrConversionTimedOut, ErrSourceTooLarge, or the caller's context
// error if it cancels while waiting.
func (c *Cache) GetOrConvert(ctx context.Context, srcPath string, key SourceKey) (string, error) {
	kh := key.hash()
	dst := filepath.Join(c.opts.Dir, kh+".epub")

	// Fast path: already converted. Refresh mtime so the mtime-ordered budget
	// eviction behaves as a real LRU — recently-read entries sort newest and
	// survive eviction.
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		now := time.Now()
		_ = os.Chtimes(dst, now, now)
		return dst, nil
	}

	// Negative cache: known DRM/deterministically-bad source.
	if err := c.negativeLookup(kh); err != nil {
		return "", err
	}

	// Singleflight via DoChan with a *detached* context: coalesced callers share
	// one conversion, so a single caller canceling its request must not abort
	// the shared work for the others (golang.org/x/sync/singleflight.Do has no
	// per-caller context, and binding it to the first caller would propagate that
	// caller's cancellation to all). The conversion stays bounded by the
	// Converter's own timeout + concurrency semaphore; the caller's context only
	// governs how long *this* caller waits (the select below). A caller that
	// gives up still leaves the conversion running to populate the cache.
	ch := c.group.DoChan(kh, func() (interface{}, error) {
		// Re-check after acquiring the singleflight slot (another caller may
		// have just produced it).
		if fi, statErr := os.Stat(dst); statErr == nil && fi.Size() > 0 {
			return dst, nil
		}
		// Convert to a temp file in the cache dir, then atomically rename in.
		tmp, tmpErr := os.CreateTemp(c.opts.Dir, "converting-*.epub")
		if tmpErr != nil {
			return "", fmt.Errorf("%w: cache temp: %v", ErrConversionFailed, tmpErr)
		}
		tmpPath := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // Convert recreates it

		if convErr := c.conv.Convert(context.WithoutCancel(ctx), srcPath, tmpPath); convErr != nil {
			_ = os.Remove(tmpPath)
			c.remember(kh, convErr)
			return "", convErr
		}
		if renErr := os.Rename(tmpPath, dst); renErr != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("%w: cache rename: %v", ErrConversionFailed, renErr)
		}
		c.enforceBudget(dst)
		return dst, nil
	})

	select {
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		return res.Val.(string), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Lookup returns a cached conversion result WITHOUT converting on miss. It lets
// cheap callers (e.g. HEAD requests) avoid kicking off a full conversion:
//   - ok == true, err == nil: a ready EPUB exists at path.
//   - err != nil: the source is negatively cached (DRM/failed); path is empty.
//   - ok == false, err == nil: cache miss — caller must decide whether to convert.
func (c *Cache) Lookup(key SourceKey) (path string, err error, ok bool) {
	kh := key.hash()
	dst := filepath.Join(c.opts.Dir, kh+".epub")
	if fi, statErr := os.Stat(dst); statErr == nil && fi.Size() > 0 {
		now := time.Now()
		_ = os.Chtimes(dst, now, now) // a HEAD hit counts as access for LRU
		return dst, nil, true
	}
	if negErr := c.negativeLookup(kh); negErr != nil {
		return "", negErr, false
	}
	return "", nil, false
}

func (c *Cache) negativeLookup(kh string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.neg[kh]
	if !ok {
		return nil
	}
	if time.Now().After(e.expiry) {
		delete(c.neg, kh)
		return nil
	}
	return e.err
}

// remember negatively caches only *deterministic* bad outcomes — DRM
// (ErrDRMProtected) and conversion failures (ErrConversionFailed: corrupt
// source, unconvertible layout, oversize output) — so repeat reads of a
// known-bad file skip reconversion for NegativeTTL. Transient outcomes
// (ErrConversionTimedOut, context cancellation/deadline, ErrSourceTooLarge) are
// deliberately NOT cached so the next read can retry — a one-off timeout under
// load must not wedge a convertible book onto the raw-fallback path.
func (c *Cache) remember(kh string, err error) {
	if !errors.Is(err, ErrDRMProtected) && !errors.Is(err, ErrConversionFailed) {
		return
	}
	c.mu.Lock()
	c.neg[kh] = negEntry{err: err, expiry: time.Now().Add(c.opts.NegativeTTL)}
	c.mu.Unlock()
}

// enforceBudget evicts the oldest (by mtime) finished cache entries until the
// total is within MaxBytes. It never evicts keep (the entry the caller is about
// to return) and ignores in-flight "converting-*" temp files, since deleting
// either would hand back a vanished path or corrupt a concurrent conversion.
// Best-effort; logs nothing (caller has no logger here).
func (c *Cache) enforceBudget(keep string) {
	entries, err := os.ReadDir(c.opts.Dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var items []item
	var total int64
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".epub" || strings.HasPrefix(name, "converting-") {
			continue // skip dirs, non-epubs, and other conversions' in-flight temps
		}
		fi, statErr := e.Info()
		if statErr != nil {
			continue
		}
		items = append(items, item{filepath.Join(c.opts.Dir, name), fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	if total <= c.opts.MaxBytes {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		if total <= c.opts.MaxBytes {
			break
		}
		if it.path == keep {
			continue // never evict the entry we're about to return
		}
		if os.Remove(it.path) == nil {
			total -= it.size
		}
	}
}

// SourceKeyFromStat builds a SourceKey from a file path + fileID, reading
// size/mtime via stat. Checksum is left empty (pass one explicitly if known).
func SourceKeyFromStat(fileID int, path string) (SourceKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return SourceKey{}, err
	}
	return SourceKey{FileID: fileID, Size: fi.Size(), ModTimeNano: fi.ModTime().UnixNano()}, nil
}

// for tests / diagnostics
func (k SourceKey) String() string { return strconv.Itoa(k.FileID) + ":" + k.hash()[:8] }

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
// cache) or ErrSourceTooLarge.
func (c *Cache) GetOrConvert(ctx context.Context, srcPath string, key SourceKey) (string, error) {
	kh := key.hash()
	dst := filepath.Join(c.opts.Dir, kh+".epub")

	// Fast path: already converted.
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		_ = os.Chtimes(dst, time.Now(), fi.ModTime()) // touch atime for LRU
		return dst, nil
	}

	// Negative cache: known DRM/bad source.
	if err := c.negativeLookup(kh); err != nil {
		return "", err
	}

	v, err, _ := c.group.Do(kh, func() (interface{}, error) {
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

		if convErr := c.conv.Convert(ctx, srcPath, tmpPath); convErr != nil {
			_ = os.Remove(tmpPath)
			c.remember(kh, convErr)
			return "", convErr
		}
		if renErr := os.Rename(tmpPath, dst); renErr != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("%w: cache rename: %v", ErrConversionFailed, renErr)
		}
		c.enforceBudget()
		return dst, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
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

// remember negatively caches only deterministic-bad outcomes (DRM, corrupt
// source). Transient failures (timeout, oversize, cancellation) are not cached
// so they can be retried.
func (c *Cache) remember(kh string, err error) {
	if !errors.Is(err, ErrDRMProtected) && !errors.Is(err, ErrConversionFailed) {
		return
	}
	c.mu.Lock()
	c.neg[kh] = negEntry{err: err, expiry: time.Now().Add(c.opts.NegativeTTL)}
	c.mu.Unlock()
}

// enforceBudget evicts the oldest (by modtime) cached EPUBs until the total is
// within MaxBytes. Best-effort; logs nothing (caller has no logger here).
func (c *Cache) enforceBudget() {
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
		if e.IsDir() || filepath.Ext(e.Name()) != ".epub" {
			continue
		}
		fi, statErr := e.Info()
		if statErr != nil {
			continue
		}
		items = append(items, item{filepath.Join(c.opts.Dir, e.Name()), fi.Size(), fi.ModTime()})
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

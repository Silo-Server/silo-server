package ebookconvert

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/privatedir"
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
	// Dir is the on-disk cache root for converted EPUBs (required). NewCache
	// refuses a directory another local account could control.
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
	// root is opts.Dir, opened once it was verified private. Every cache read
	// and write goes through it, so neither a symlinked entry nor a later swap
	// of the dir's path can redirect them outside the verified dir.
	root *os.Root

	group singleflight.Group

	mu  sync.Mutex
	neg map[string]negEntry // key hash -> negative result
}

type negEntry struct {
	err    error
	expiry time.Time
}

// NewCache creates the cache dir, or verifies an existing one is private to
// this process's user, and returns a ready Cache.
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
	root, err := privatedir.OpenRoot(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("ebookconvert: cache dir %w", err)
	}
	return &Cache{conv: conv, opts: opts, root: root, neg: make(map[string]negEntry)}, nil
}

// GetOrConvert returns the cached EPUB for srcPath/key, open for reading,
// converting on miss; the caller closes it. Concurrent calls for the same key
// collapse to a single conversion. Returns ErrDRMProtected /
// ErrConversionFailed (also served from the negative cache),
// ErrConversionTimedOut, ErrSourceTooLarge, or the caller's context error if it
// cancels while waiting.
func (c *Cache) GetOrConvert(ctx context.Context, srcPath string, key SourceKey) (*os.File, error) {
	kh := key.hash()
	name := kh + ".epub"

	// Fast path: already converted.
	if f, ok := c.openEntry(name); ok {
		return f, nil
	}

	// Negative cache: known DRM/deterministically-bad source.
	if err := c.negativeLookup(kh); err != nil {
		return nil, err
	}

	// Singleflight via DoChan with a *detached* context: coalesced callers share
	// one conversion, so a single caller canceling its request must not abort
	// the shared work for the others (golang.org/x/sync/singleflight.Do has no
	// per-caller context, and binding it to the first caller would propagate that
	// caller's cancellation to all). The conversion stays bounded by the
	// Converter's own timeout + concurrency semaphore; the caller's context only
	// governs how long *this* caller waits (the select below). A caller that
	// gives up still leaves the conversion running to populate the cache.
	ch := c.group.DoChan(kh, func() (any, error) {
		// Re-check after acquiring the singleflight slot (another caller may
		// have just produced it).
		if f, ok := c.openEntry(name); ok {
			_ = f.Close()
			return nil, nil
		}
		// Convert into a private scratch dir, then install through the root.
		scratch, scratchErr := os.MkdirTemp(c.conv.opts.WorkRoot, "ebookconvert-out-")
		if scratchErr != nil {
			return nil, fmt.Errorf("%w: cache temp: %v", ErrConversionFailed, scratchErr) //nolint:errorlint // callers classify by the verdict sentinel; the filesystem error is only reported
		}
		defer func() { _ = os.RemoveAll(scratch) }()
		out := filepath.Join(scratch, "out.epub")

		if convErr := c.conv.Convert(context.WithoutCancel(ctx), srcPath, out); convErr != nil {
			c.remember(kh, convErr)
			return nil, convErr
		}
		if installErr := c.install(out, name); installErr != nil {
			return nil, fmt.Errorf("%w: cache install: %v", ErrConversionFailed, installErr) //nolint:errorlint // callers classify by the verdict sentinel; the filesystem error is only reported
		}
		c.enforceBudget(name)
		return nil, nil
	})

	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		// Each caller opens its own descriptor: coalesced callers cannot share
		// one file offset.
		f, ok := c.openEntry(name)
		if !ok {
			return nil, fmt.Errorf("%w: converted entry vanished", ErrConversionFailed)
		}
		return f, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Lookup returns a cached conversion result WITHOUT converting on miss. It lets
// cheap callers (e.g. HEAD requests) avoid kicking off a full conversion:
//   - ok == true, err == nil: f is the ready EPUB, open for reading; the
//     caller closes it.
//   - err != nil: the source is negatively cached (DRM/failed); f is nil.
//   - ok == false, err == nil: cache miss — caller must decide whether to convert.
func (c *Cache) Lookup(key SourceKey) (f *os.File, err error, ok bool) {
	kh := key.hash()
	if f, ok := c.openEntry(kh + ".epub"); ok {
		return f, nil, true // a HEAD hit counts as access for LRU
	}
	if negErr := c.negativeLookup(kh); negErr != nil {
		return nil, negErr, false
	}
	return nil, nil, false
}

// openEntry opens a finished cache entry through the cache root, so a symlink
// cannot redirect the read outside the cache dir, and accepts only a non-empty
// regular file. A hit refreshes the entry's mtime so the mtime-ordered budget
// eviction behaves as a real LRU: recently read entries sort newest and survive.
func (c *Cache) openEntry(name string) (*os.File, bool) {
	f, err := c.root.Open(name)
	if err != nil {
		return nil, false
	}
	if fi, statErr := f.Stat(); statErr != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		_ = f.Close()
		return nil, false
	}
	now := time.Now()
	_ = c.root.Chtimes(name, now, now)
	return f, true
}

// install copies the finished conversion at src into the cache as name. The
// temp file, the rename, and every other cache write go through the root, so
// they stay in the verified cache dir even if its path is later replaced.
func (c *Cache) install(src, name string) error {
	in, err := os.Open(src) //nolint:gosec // src is the converter's output in this conversion's private scratch dir
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmpName := "converting-" + rand.Text() + ".epub"
	out, err := c.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = c.root.Rename(tmpName, name)
	}
	if err != nil {
		_ = c.root.Remove(tmpName)
	}
	return err
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
// either would hand back a vanished entry or corrupt a concurrent conversion.
// Best-effort; logs nothing (caller has no logger here).
func (c *Cache) enforceBudget(keep string) {
	dir, err := c.root.Open(".")
	if err != nil {
		return
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return
	}
	type item struct {
		name string
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
		fi, statErr := c.root.Lstat(name)
		if statErr != nil || !fi.Mode().IsRegular() {
			continue
		}
		items = append(items, item{name, fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	if total <= c.opts.MaxBytes {
		return
	}
	slices.SortFunc(items, func(a, b item) int { return a.mod.Compare(b.mod) })
	for _, it := range items {
		if total <= c.opts.MaxBytes {
			break
		}
		if it.name == keep {
			continue // never evict the entry we're about to return
		}
		if c.root.Remove(it.name) == nil {
			total -= it.size
		}
	}
}

// SourceKeyFromStat builds a SourceKey from a file path + fileID, reading
// size/mtime via stat. path is fspath-encoded. Checksum is left empty (pass
// one explicitly if known).
func SourceKeyFromStat(fileID int, path string) (SourceKey, error) {
	fi, err := fspath.Stat(path)
	if err != nil {
		return SourceKey{}, err
	}
	return SourceKey{FileID: fileID, Size: fi.Size(), ModTimeNano: fi.ModTime().UnixNano()}, nil
}

// for tests / diagnostics
func (k SourceKey) String() string { return strconv.Itoa(k.FileID) + ":" + k.hash()[:8] }

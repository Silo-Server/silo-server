package keyframes

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"
)

// ErrUnverified means the file's index passed the quick check and is being
// checked against every cluster in the background; until that finishes it
// isn't used. It is reported as ErrNoIndex.
var ErrUnverified = fmt.Errorf("%w: keyframe index not verified yet", ErrNoIndex)

const (
	// maxCachedFiles bounds the process-wide result cache. An index is a few
	// kilobytes to a few hundred, one float per keyframe.
	maxCachedFiles = 256
	// maxQueuedChecks bounds the full checks waiting or running. A file that
	// finds the queue full is checked on a later playback.
	maxQueuedChecks = 16
)

type cacheKey struct {
	path    string
	size    int64
	modTime time.Time
}

type cacheEntry struct {
	key   cacheKey
	index Index
	err   error
}

// indexCache holds checked results, including files without a usable index,
// so a later playback or a second viewer doesn't read the file again. An
// entry is keyed by the file's size and modification time, so a replaced file
// is read afresh. Checks in progress are tracked apart from the results, so
// evicting results never loses track of one.
var indexCache = struct {
	sync.Mutex
	order    *list.List
	entries  map[cacheKey]*list.Element
	inFlight map[cacheKey]*check
}{order: list.New(), entries: map[cacheKey]*list.Element{}, inFlight: map[cacheKey]*check{}}

// check is a full check queued or running in the background. It reads only
// while someone wants its result: each Load that finds the file unverified
// holds it until its holder context is done, and the check stops once no one
// holds it (#2453). A playback's session holds it, so stopping playback stops
// reading the file.
type check struct {
	ctx     context.Context
	cancel  context.CancelFunc
	holders int // guarded by indexCache
}

// holdLocked keeps c, the check for key, running until holder is done. Call
// with indexCache held.
func (c *check) holdLocked(key cacheKey, holder context.Context) {
	c.holders++
	context.AfterFunc(holder, func() {
		indexCache.Lock()
		defer indexCache.Unlock()
		c.holders--
		if c.holders == 0 {
			// End it now, so a playback that starts while the worker unwinds
			// queues a new check rather than holding this one.
			c.cancel()
			c.endLocked(key)
		}
	})
}

// verifySlots runs one full check at a time: each reads every block header
// of a file.
var verifySlots = make(chan struct{}, 1)

// Load returns the keyframe index of the media file at path once it has been
// checked against every cluster. The first call for a file runs the quick
// check and, if it passes, queues the full check in the background and
// returns ErrUnverified; calls after it finishes get the result from the
// cache. A file without a usable index, including any that isn't Matroska or
// WebM or is malformed, gets ErrNoIndex.
//
// holder is how long the caller wants the background check: a playback's
// session. The check stops reading once every caller that found the file
// unverified has gone, and isn't cached, so a later playback starts it again.
func Load(holder context.Context, path string) (Index, error) {
	return load(holder, path, false)
}

// LoadVerified is Load that runs the full check itself instead of in the
// background. The check stops when ctx is done, and a stopped check isn't
// cached.
func LoadVerified(ctx context.Context, path string) (Index, error) {
	return load(ctx, path, true)
}

func load(ctx context.Context, path string, wait bool) (Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return Index{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Index{}, err
	}
	key := cacheKey{path: path, size: info.Size(), modTime: info.ModTime()}

	if !wait {
		if idx, err, ok := cachedHolding(key, ctx); ok {
			return idx, err
		}
	} else if idx, err, ok := cached(key); ok && !errors.Is(err, ErrUnverified) {
		return idx, err
	}

	if wait {
		idx, err := VerifyMatroska(contextReaderAt{ctx: ctx, r: f}, info.Size())
		if err != nil && transient(err) {
			return Index{}, err
		}
		idx, err = settle(idx, err)
		store(key, idx, err)
		return idx, err
	}

	if idx, err := ReadMatroska(f, info.Size()); err != nil {
		if transient(err) {
			return Index{}, err
		}
		idx, err = settle(idx, err)
		store(key, idx, err)
		return idx, err
	}
	if c := startCheck(key, ctx); c != nil {
		go verifyInBackground(key, c)
		return Index{}, ErrUnverified
	}
	// Not queued: a check may have started or finished since the lookup
	// above; hold it, or return its result rather than ErrUnverified.
	if idx, err, ok := cachedHolding(key, ctx); ok {
		return idx, err
	}
	return Index{}, ErrUnverified
}

// transient reports an error reading the file, which says nothing lasting
// about it: a later playback tries again. Anything else describes the file.
func transient(err error) bool {
	return errors.As(err, new(*fs.PathError)) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// contextReaderAt stops a full check once its caller has gone: every read
// fails with the context's error.
type contextReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (c contextReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.ReadAt(p, off)
}

// settle turns a lasting failure into ErrNoIndex: the file isn't Matroska, is
// malformed, or has no complete index.
func settle(idx Index, err error) (Index, error) {
	if err == nil || errors.Is(err, ErrNoIndex) {
		return idx, err
	}
	return Index{}, fmt.Errorf("%w: %w", ErrNoIndex, err)
}

// startCheck records a full check for key, held by holder, unless one is
// already queued or the queue is full.
func startCheck(key cacheKey, holder context.Context) *check {
	indexCache.Lock()
	defer indexCache.Unlock()
	if _, ok := indexCache.inFlight[key]; ok || len(indexCache.inFlight) >= maxQueuedChecks {
		return nil
	}
	if _, ok := indexCache.entries[key]; ok {
		return nil // a check finished meanwhile
	}
	c := &check{}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.holdLocked(key, holder)
	indexCache.inFlight[key] = c
	return c
}

// endLocked removes c from the checks in flight, unless a newer check for key
// has replaced it. Call with indexCache held.
func (c *check) endLocked(key cacheKey) {
	if indexCache.inFlight[key] == c {
		delete(indexCache.inFlight, key)
	}
}

func verifyInBackground(key cacheKey, c *check) {
	defer c.cancel()
	select {
	case verifySlots <- struct{}{}:
	case <-c.ctx.Done():
		// Everyone who wanted it left while it waited for a slot; the last
		// one ended it.
		return
	}
	defer func() { <-verifySlots }()

	idx, err := func() (Index, error) {
		f, err := os.Open(key.path)
		if err != nil {
			return Index{}, err
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil {
			return Index{}, err
		}
		if info.Size() != key.size || !info.ModTime().Equal(key.modTime) {
			return Index{}, &fs.PathError{Op: "verify", Path: key.path, Err: errors.New("file changed during the check")}
		}
		return VerifyMatroska(contextReaderAt{ctx: c.ctx, r: f}, info.Size())
	}()

	if err != nil && transient(err) {
		if c.ctx.Err() != nil {
			slog.Info("keyframe index: check stopped; no playback needs it", "component", "playback", "path", key.path)
		} else {
			slog.Warn("keyframe index: check file", "component", "playback", "path", key.path, "error", err)
		}
		indexCache.Lock()
		c.endLocked(key) // a later playback tries again
		indexCache.Unlock()
		return
	}
	idx, err = settle(idx, err)
	// Publish the result and end the check together, so no Load sees
	// neither and queues the file again.
	indexCache.Lock()
	storeLocked(key, idx, err)
	c.endLocked(key)
	indexCache.Unlock()
}

// cachedHolding is cached for a caller that will use a running check's
// result: it holds the check until holder is done.
func cachedHolding(key cacheKey, holder context.Context) (Index, error, bool) {
	indexCache.Lock()
	defer indexCache.Unlock()
	if c, ok := indexCache.inFlight[key]; ok {
		c.holdLocked(key, holder)
		return Index{}, ErrUnverified, true
	}
	return cachedLocked(key)
}

func cached(key cacheKey) (Index, error, bool) {
	indexCache.Lock()
	defer indexCache.Unlock()
	return cachedLocked(key)
}

func cachedLocked(key cacheKey) (Index, error, bool) {
	if _, ok := indexCache.inFlight[key]; ok {
		return Index{}, ErrUnverified, true
	}
	el, ok := indexCache.entries[key]
	if !ok {
		return Index{}, nil, false
	}
	indexCache.order.MoveToFront(el)
	entry, _ := el.Value.(*cacheEntry)
	return entry.index, entry.err, true
}

func store(key cacheKey, idx Index, err error) {
	indexCache.Lock()
	defer indexCache.Unlock()
	storeLocked(key, idx, err)
}

func storeLocked(key cacheKey, idx Index, err error) {
	if el, ok := indexCache.entries[key]; ok {
		entry, _ := el.Value.(*cacheEntry)
		entry.index, entry.err = idx, err
		indexCache.order.MoveToFront(el)
		return
	}
	indexCache.entries[key] = indexCache.order.PushFront(&cacheEntry{key: key, index: idx, err: err})
	for indexCache.order.Len() > maxCachedFiles {
		oldest := indexCache.order.Back()
		indexCache.order.Remove(oldest)
		if entry, ok := oldest.Value.(*cacheEntry); ok {
			delete(indexCache.entries, entry.key)
		}
	}
}

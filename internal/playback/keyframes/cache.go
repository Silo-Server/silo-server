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

// cacheKey names a file's revision: a local file by its path, size and
// modification time, a Source by its identity and size.
type cacheKey struct {
	path     string
	identity string
	size     int64
	modTime  time.Time
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
	inFlight map[cacheKey]struct{}
}{order: list.New(), entries: map[cacheKey]*list.Element{}, inFlight: map[cacheKey]struct{}{}}

// verifySlots runs one full check at a time: each reads every block header
// of a file.
var verifySlots = make(chan struct{}, 1)

// Load returns the keyframe index of the media file at path once it has been
// checked against every cluster. The first call for a file runs the quick
// check and, if it passes, queues the full check in the background and
// returns ErrUnverified; calls after it finishes get the result from the
// cache. A file without a usable index, including any that isn't Matroska or
// WebM or is malformed, gets ErrNoIndex.
func Load(path string) (Index, error) {
	return load(context.Background(), path, false)
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

	if idx, err, ok := cached(key); ok && (!wait || !errors.Is(err, ErrUnverified)) {
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
	if startCheck(key) {
		go verifyInBackground(key)
		return Index{}, ErrUnverified
	}
	// Not queued: a check may have finished since the lookup above, so
	// return its result rather than ErrUnverified.
	if idx, err, ok := cached(key); ok {
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

// startCheck records a full check for key, unless one is already queued or
// the queue is full.
func startCheck(key cacheKey) bool {
	indexCache.Lock()
	defer indexCache.Unlock()
	if _, ok := indexCache.inFlight[key]; ok || len(indexCache.inFlight) >= maxQueuedChecks {
		return false
	}
	if _, ok := indexCache.entries[key]; ok {
		return false // a check finished meanwhile
	}
	indexCache.inFlight[key] = struct{}{}
	return true
}

func verifyInBackground(key cacheKey) {
	verifySlots <- struct{}{}
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
		return VerifyMatroska(f, info.Size())
	}()

	if err != nil && transient(err) {
		slog.Warn("keyframe index: check file", "component", "playback", "path", key.path, "error", err)
		indexCache.Lock()
		delete(indexCache.inFlight, key) // a later playback tries again
		indexCache.Unlock()
		return
	}
	idx, err = settle(idx, err)
	// Publish the result and end the check together, so no Load sees
	// neither and queues the file again.
	indexCache.Lock()
	storeLocked(key, idx, err)
	delete(indexCache.inFlight, key)
	indexCache.Unlock()
}

func cached(key cacheKey) (Index, error, bool) {
	indexCache.Lock()
	defer indexCache.Unlock()
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

// Source is a media file read through something other than its path: on a
// transcode node with core source access, a source relayed from the core
// (internal/nodesource), whose loopback URL changes with every job while its
// identity stays the same.
type Source struct {
	// Identity names the source's revision; it keys the cache, so it must
	// change whenever the bytes do.
	Identity string
	Size     int64
	// Context ends when the source is released or withdrawn. It bounds the
	// background check that LoadSource starts on the source's behalf.
	Context context.Context
	// Open opens the source for reading, bounded by ctx. Its errors, and
	// those of the reader, are *fs.PathError (see transient).
	Open func(ctx context.Context) (ReaderAtCloser, error)
	// OpenSequential is Open for the full check, which reads the whole file
	// front to back: a reader that streams it rather than fetching each
	// block header on its own. Nil uses Open.
	OpenSequential func(ctx context.Context) (ReaderAtCloser, error)
}

// openForCheck opens the source for the full check.
func (s Source) openForCheck(ctx context.Context) (ReaderAtCloser, error) {
	if s.OpenSequential != nil {
		return s.OpenSequential(ctx)
	}
	return s.Open(ctx)
}

// ReaderAtCloser is random access to a Source.
type ReaderAtCloser interface {
	io.ReaderAt
	io.Closer
}

// LoadSource is Load for a Source: the quick check here, the full check in the
// background, ErrUnverified until it passes.
func LoadSource(ctx context.Context, source Source) (Index, error) {
	return loadSource(ctx, source, false)
}

// LoadSourceVerified is LoadVerified for a Source: it runs the full check
// itself, stopping when ctx is done. A check that stopped, or whose reads
// failed, returns its error and isn't cached.
func LoadSourceVerified(ctx context.Context, source Source) (Index, error) {
	return loadSource(ctx, source, true)
}

func loadSource(ctx context.Context, source Source, wait bool) (Index, error) {
	if source.Identity == "" || source.Size <= 0 || source.Context == nil || source.Open == nil {
		return Index{}, sourceError(errors.New("incomplete source"))
	}
	key := cacheKey{identity: source.Identity, size: source.Size}
	if idx, err, ok := cached(key); ok && (!wait || !errors.Is(err, ErrUnverified)) {
		return idx, err
	}
	if err := source.Context.Err(); err != nil {
		return Index{}, sourceError(context.Cause(source.Context))
	}
	open := source.Open
	if wait {
		open = source.openForCheck
	}
	r, err := open(ctx)
	if err != nil {
		return Index{}, err
	}
	defer func() { _ = r.Close() }()

	if wait {
		idx, err := VerifyMatroska(contextReaderAt{ctx: ctx, r: r}, source.Size)
		if err != nil && transient(err) {
			return Index{}, err
		}
		idx, err = settle(idx, err)
		store(key, idx, err)
		return idx, err
	}

	if idx, err := ReadMatroska(contextReaderAt{ctx: ctx, r: r}, source.Size); err != nil {
		if transient(err) {
			return Index{}, err
		}
		idx, err = settle(idx, err)
		store(key, idx, err)
		return idx, err
	}
	if startCheck(key) {
		go verifySourceInBackground(key, source)
		return Index{}, ErrUnverified
	}
	if idx, err, ok := cached(key); ok {
		return idx, err
	}
	return Index{}, ErrUnverified
}

// sourceError reports a Source that cannot be read as the read failure it is.
func sourceError(err error) error {
	return &fs.PathError{Op: "index", Path: "source", Err: err}
}

// verifySourceInBackground is verifyInBackground for a Source. The check
// waits for its turn and runs within the source's life: a source released or
// withdrawn meanwhile ends it, uncached, and a later playback tries again.
func verifySourceInBackground(key cacheKey, source Source) {
	idx, err := func() (Index, error) {
		select {
		case verifySlots <- struct{}{}:
			defer func() { <-verifySlots }()
		case <-source.Context.Done():
			return Index{}, sourceError(context.Cause(source.Context))
		}
		r, err := source.openForCheck(source.Context)
		if err != nil {
			return Index{}, err
		}
		defer func() { _ = r.Close() }()
		return VerifyMatroska(contextReaderAt{ctx: source.Context, r: r}, source.Size)
	}()
	if err != nil && transient(err) {
		slog.Warn("keyframe index: check source", "component", "playback", "source", source.Identity, "error", err)
		indexCache.Lock()
		delete(indexCache.inFlight, key) // a later playback tries again
		indexCache.Unlock()
		return
	}
	idx, err = settle(idx, err)
	indexCache.Lock()
	storeLocked(key, idx, err)
	delete(indexCache.inFlight, key)
	indexCache.Unlock()
}

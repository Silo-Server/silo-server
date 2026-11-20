package nodesource

import (
	"container/list"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Readers of core-access sources serve the node's Go-side consumers:
// keyframe and subtitle indexes, subtitle packets, tone-map hashes, Matroska
// Cues for trickplay. Their reads are small and scattered, many of them at
// once (burn-in staging keeps 64 in flight), and the same few regions (the
// header, the Cues) are read again by the next consumer. Every reader of a
// revision shares its relay entry's page cache, fetches outside any lock, and
// shares a page another reader is already fetching.
const (
	// readerPageBytes is the unit small reads fetch and cache in. Nearby
	// reads share one request.
	readerPageBytes = 64 << 10
	// readerCachePages bounds an entry's page cache: 4 MiB a source
	// revision, enough for a large file's Cues and header read by each
	// index in turn, while a node with 64 sources open holds 256 MiB at
	// most.
	readerCachePages = 64
	// readerRangeBytes bounds one direct fetch of a read larger than a
	// page, which is not cached.
	readerRangeBytes = 1 << 20
	// readerMaxBytes bounds what one reader fetches, with twice the source's
	// size: a consumer stuck in a loop fails rather than streaming a film.
	readerMaxBytes = int64(64) << 30
)

// readerFetchSlots bounds the upstream requests all readers in the process
// have in flight. It is sized so no one consumer fills it: a burn-in staging
// keeps 64 reads in flight, a subtitle rendition window up to 16, and the
// tone-map hash, keyframe index loads and concurrent trickplay Cues reads a
// few each, so a playback start is not queued behind background work. Each
// slot is one request of at most 1 MiB on a pooled connection.
var readerFetchSlots = make(chan struct{}, 128)

// ErrReaderLimit is a source read that would exceed its reader's budget.
var ErrReaderLimit = errors.New("nodesource: source reader budget exceeded")

// pageCache is an entry's cache of 64 KiB pages, least recently used first
// out, with the fetches in flight so concurrent reads of one page share one.
type pageCache struct {
	e *relayEntry

	mu       sync.Mutex
	pages    map[int64]*list.Element
	order    *list.List
	inflight map[int64]*pageFetch
}

type cachedPage struct {
	offset int64
	data   []byte
}

// pageFetch is one page being fetched; done closes when data or err is set.
type pageFetch struct {
	done chan struct{}
	data []byte
	err  error
}

func newPageCache(e *relayEntry) *pageCache {
	return &pageCache{e: e, pages: map[int64]*list.Element{}, order: list.New(), inflight: map[int64]*pageFetch{}}
}

// page returns the page at offset (a multiple of readerPageBytes), from the
// cache, from a fetch in flight, or from a new fetch that is charged to r.
// The fetch runs on its own, bounded by the entry and the retry window, so a
// reader that gives up does not fail the others waiting on it.
func (c *pageCache) page(ctx context.Context, r *rangeReader, offset int64) ([]byte, error) {
	c.mu.Lock()
	if el := c.pages[offset]; el != nil {
		c.order.MoveToFront(el)
		page, _ := el.Value.(*cachedPage)
		c.mu.Unlock()
		return page.data, nil
	}
	f := c.inflight[offset]
	if f == nil {
		length := min(int64(readerPageBytes), c.e.size-offset)
		if err := r.charge(length); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		f = &pageFetch{done: make(chan struct{})}
		c.inflight[offset] = f
		go c.fill(f, offset, length)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.data, f.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (c *pageCache) fill(f *pageFetch, offset, length int64) {
	f.data, f.err = fetchBytes(c.e.ctx, c.e, offset, length)
	c.mu.Lock()
	delete(c.inflight, offset)
	if f.err == nil && c.e.ctx.Err() == nil {
		c.pages[offset] = c.order.PushFront(&cachedPage{offset: offset, data: f.data})
		for c.order.Len() > readerCachePages {
			last := c.order.Back()
			c.order.Remove(last)
			if page, ok := last.Value.(*cachedPage); ok {
				delete(c.pages, page.offset)
			}
		}
	}
	c.mu.Unlock()
	close(f.done)
}

// fetchBytes reads [off, off+length) of e's source on one of the process's
// reader slots.
func fetchBytes(ctx context.Context, e *relayEntry, off, length int64) ([]byte, error) {
	select {
	case readerFetchSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	defer func() { <-readerFetchSlots }()
	body, err := e.fetchRange(ctx, off, length)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	data := make([]byte, length)
	if _, err := io.ReadFull(body, data); err != nil {
		return nil, err
	}
	return data, nil
}

// rangeReader is random access to a core-access source (fspath.Reader). It
// reads through its entry's page cache, fetching reads larger than a page
// directly in ranges of at most 1 MiB, and fetches at most twice the
// source's size. It is safe for concurrent use. Every error is an
// *fs.PathError naming the source by its entity tag, which callers such as
// the keyframe index treat as a failed read rather than a verdict on the
// file.
type rangeReader struct {
	e        *relayEntry
	ctx      context.Context //nolint:containedctx // bounds the reader's reads, as the caller's context
	cancel   context.CancelFunc
	maxBytes int64
	fetched  atomic.Int64
	closed   atomic.Bool
}

// newReader opens a reader of e's source bounded by ctx and the entry.
func (e *relayEntry) newReader(ctx context.Context) (*rangeReader, error) {
	if err := e.Err(); err != nil {
		return nil, e.readError(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.ctx, cancel)
	return &rangeReader{e: e, ctx: ctx, maxBytes: min(readerMaxBytes, 2*max(e.size, readerPageBytes)), cancel: func() {
		stop()
		cancel()
	}}, nil
}

// charge counts n bytes against the reader's budget.
func (r *rangeReader) charge(n int64) error {
	if r.fetched.Add(n) > r.maxBytes {
		return ErrReaderLimit
	}
	return nil
}

// Stat describes the source: its granted size and modification time.
func (r *rangeReader) Stat() (os.FileInfo, error) { return sourceInfo{e: r.e}, nil }

// Close releases the reader. Reads in flight end.
func (r *rangeReader) Close() error {
	if r.closed.CompareAndSwap(false, true) {
		r.cancel()
	}
	return nil
}

// ReadAt reads len(p) bytes at off, or up to the end of the source with
// io.EOF.
func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	available, err := r.readable(len(p), off)
	if available == 0 {
		return 0, err
	}
	copied := int64(0)
	for copied < available {
		at := off + copied
		remaining := available - copied
		pageAt := at / readerPageBytes * readerPageBytes
		if remaining > readerPageBytes {
			// A large read takes a direct range instead of filling the
			// cache with pages nobody reads again.
			n := min(remaining, readerRangeBytes)
			if err := r.charge(n); err != nil {
				return int(copied), r.e.readError(err)
			}
			data, err := fetchBytes(r.ctx, r.e, at, n)
			if err != nil {
				return int(copied), r.e.readError(r.cause(err))
			}
			copied += int64(copy(p[copied:available], data))
			continue
		}
		data, err := r.e.pages.page(r.ctx, r, pageAt)
		if err != nil {
			return int(copied), r.e.readError(r.cause(err))
		}
		copied += int64(copy(p[copied:available], data[at-pageAt:]))
	}
	if available < int64(len(p)) {
		return int(copied), io.EOF
	}
	return int(copied), nil
}

// readable checks a read of n bytes at off and returns how many of them the
// source holds: 0 and io.EOF at or past its end, 0 for n == 0, or 0 and the
// reason the reader may not serve the read. Nothing is served once the core
// withdrew the source, its jobs released it, or the reader was closed.
func (r *rangeReader) readable(n int, off int64) (int64, error) {
	if r.closed.Load() {
		return 0, r.e.readError(ErrClosed)
	}
	if err := r.e.Err(); err != nil {
		return 0, r.e.readError(err)
	}
	if err := r.ctx.Err(); err != nil {
		return 0, r.e.readError(context.Cause(r.ctx))
	}
	if off < 0 {
		return 0, r.e.readError(errors.New("negative offset"))
	}
	if n == 0 {
		return 0, nil
	}
	if off >= r.e.size {
		return 0, io.EOF
	}
	return min(int64(n), r.e.size-off), nil
}

// cause prefers the entry's end, then the reader's, to a read's own error.
func (r *rangeReader) cause(err error) error {
	if entryErr := r.e.Err(); entryErr != nil {
		return entryErr
	}
	if r.ctx.Err() != nil {
		return context.Cause(r.ctx)
	}
	return err
}

// readError wraps err as the *fs.PathError every source read failure is,
// naming the source by its entity tag rather than its capability URL.
//
// A failed read never carries io.EOF or io.ErrUnexpectedEOF: parsers take
// those for the end of their data, and a fetch cut mid-transfer fails with
// one. The source did not end there, the read did, so the error keeps the
// text and loses the identity. The real end of a source is a bare io.EOF.
func (e *relayEntry) readError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		err = cutReadError{text: err.Error()}
	}
	return &fs.PathError{Op: opRead, Path: "source " + e.etag, Err: err}
}

// cutReadError is a source read that ended early.
type cutReadError struct{ text string }

func (e cutReadError) Error() string { return "read ended early: " + e.text }

// sourceInfo is a core-access source's os.FileInfo: a read-only regular file
// of the granted size and modification time, the nanoseconds the core's stat
// returned, so a revision the core recorded from its own stat matches.
type sourceInfo struct{ e *relayEntry }

func (i sourceInfo) Name() string       { return "source " + i.e.etag }
func (i sourceInfo) Size() int64        { return i.e.size }
func (i sourceInfo) Mode() fs.FileMode  { return 0o444 }
func (i sourceInfo) ModTime() time.Time { return i.e.modTime }
func (i sourceInfo) IsDir() bool        { return false }
func (i sourceInfo) Sys() any           { return nil }

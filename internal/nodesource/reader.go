package nodesource

import (
	"container/list"
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
	"time"
)

// ErrReaderLimit is a source read that would exceed its reader's bounds.
var ErrReaderLimit = errors.New("nodesource: source reader budget exceeded")

// readerSlots bounds the source readers open at once in the process. A
// reader opened while all are taken waits for one (acquireReaderSlot).
var readerSlots = make(chan struct{}, 4)

// Reader bounds. A reader serves container-index work (keyframe indexes,
// subtitle hashes): small scattered reads, cached in pages, and the odd large
// one, fetched in ranges.
const (
	readerPageBytes  = 64 << 10
	readerCacheBytes = 2 << 20
	readerRangeBytes = 1 << 20
	readerMaxBytes   = int64(64) << 30
	readerTimeout    = 2 * time.Minute
)

// Reader is random access to a source for Go-side consumers.
type Reader interface {
	io.ReaderAt
	io.Closer
}

// RangeReader reads a core-access source through fetchRange: 64 KiB pages
// in a 2 MiB LRU, reads larger than a page fetched directly in ranges of at
// most 1 MiB. It fetches at most twice the source's size (64 GiB at most)
// and lives at most two minutes. Every error is an *fs.PathError, which
// callers such as the keyframe index treat as a read failure rather than a
// verdict on the file.
type RangeReader struct {
	h        *Handle
	ctx      context.Context
	cancel   context.CancelFunc
	maxBytes int64

	mu         sync.Mutex
	once       sync.Once
	pages      map[int64]*list.Element
	order      *list.List
	cacheBytes int64
	fetched    int64
}

type readerPage struct {
	offset int64
	data   []byte
}

// ReaderAt opens a reader of the handle's source, bounded by ctx. With local
// access it is the approved file itself, checked against the granted
// revision.
func (h *Handle) ReaderAt(ctx context.Context) (Reader, error) {
	if err := h.Err(); err != nil {
		return nil, h.readError(err)
	}
	if !h.remote {
		f, err := openNonblocking(h.path)
		if err != nil {
			return nil, h.readError(err)
		}
		info, err := f.Stat()
		if err != nil || !sameRevision(info, h.size, RevisionMtime(h.modTime)) {
			_ = f.Close()
			return nil, h.readError(ErrLocalMismatch)
		}
		return f, nil
	}
	if err := h.acquireReaderSlot(ctx); err != nil {
		return nil, err
	}
	return h.newRangeReader(ctx, readerTimeout), nil
}

// newRangeReader returns a reader of the handle's source on the process slot
// its caller took, bounded by ctx, the handle's life and timeout.
func (h *Handle) newRangeReader(ctx context.Context, timeout time.Duration) *RangeReader {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	stop := context.AfterFunc(h.ctx, cancel)
	return &RangeReader{
		h: h, ctx: bounded, maxBytes: min(readerMaxBytes, 2*h.size),
		cancel: func() {
			stop()
			cancel()
		},
		pages: make(map[int64]*list.Element), order: list.New(),
	}
}

// acquireReaderSlot takes one of the process's reader slots, waiting for one
// while ctx and the handle live. A burst of readers (tone-mapped starts, plan
// rebuilds) queues rather than failing.
func (h *Handle) acquireReaderSlot(ctx context.Context) error {
	select {
	case readerSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return h.readError(context.Cause(ctx))
	case <-h.ctx.Done():
		return h.readError(context.Cause(h.ctx))
	}
}

// Close releases the reader and its process slot.
func (r *RangeReader) Close() error {
	r.once.Do(func() {
		r.cancel()
		r.mu.Lock()
		r.pages, r.cacheBytes = nil, 0
		r.order.Init()
		r.mu.Unlock()
		<-readerSlots
	})
	return nil
}

// ReadAt reads len(p) bytes at off, or up to the end of the source with
// io.EOF.
func (r *RangeReader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	available, err := r.readable(len(p), off)
	if available == 0 {
		return 0, err
	}
	size := r.h.size
	copied := int64(0)
	for copied < available {
		at := off + copied
		pageAt := at / readerPageBytes * readerPageBytes
		if page := r.pages[pageAt]; page != nil {
			r.order.MoveToFront(page)
			cached, _ := page.Value.(*readerPage)
			copied += int64(copy(p[copied:available], cached.data[at-pageAt:]))
			continue
		}
		// Large reads take a direct range; small ones an aligned page, so
		// nearby small reads share one request.
		if available-copied > readerPageBytes {
			data, err := r.fetch(at, min(available-copied, readerRangeBytes))
			if err != nil {
				return int(copied), err
			}
			copied += int64(copy(p[copied:available], data))
			continue
		}
		data, err := r.fetch(pageAt, min(readerPageBytes, size-pageAt))
		if err != nil {
			return int(copied), err
		}
		r.cache(pageAt, data)
		copied += int64(copy(p[copied:available], data[at-pageAt:]))
	}
	if available < int64(len(p)) {
		return int(copied), io.EOF
	}
	return int(copied), nil
}

// readable checks a read of n bytes at off, under r.mu, and returns how many
// of them the source holds: 0 and io.EOF at or past its end, 0 for n == 0,
// or 0 and the reason the reader may not serve the read. Even a cached page
// is not served once the core withdrew the source, its job released it, or
// the reader's deadline passed.
func (r *RangeReader) readable(n int, off int64) (int64, error) {
	if r.pages == nil {
		return 0, r.h.readError(ErrClosed)
	}
	if err := r.h.Err(); err != nil {
		return 0, r.h.readError(err)
	}
	if err := r.ctx.Err(); err != nil {
		return 0, r.h.readError(context.Cause(r.ctx))
	}
	if off < 0 {
		return 0, r.h.readError(errors.New("negative offset"))
	}
	if n == 0 {
		return 0, nil
	}
	if off >= r.h.size {
		return 0, io.EOF
	}
	return min(int64(n), r.h.size-off), nil
}

func (r *RangeReader) cache(offset int64, data []byte) {
	r.pages[offset] = r.order.PushFront(&readerPage{offset: offset, data: data})
	r.cacheBytes += int64(len(data))
	for r.cacheBytes > readerCacheBytes {
		last := r.order.Back()
		page, _ := last.Value.(*readerPage)
		delete(r.pages, page.offset)
		r.cacheBytes -= int64(len(page.data))
		r.order.Remove(last)
	}
}

func (r *RangeReader) fetch(off, length int64) ([]byte, error) {
	if length > r.maxBytes-r.fetched {
		return nil, r.h.readError(ErrReaderLimit)
	}
	r.fetched += length
	body, err := r.h.fetchRange(r.ctx, off, length)
	if err != nil {
		return nil, r.h.readError(err)
	}
	defer func() { _ = body.Close() }()
	data := make([]byte, length)
	if _, err := io.ReadFull(body, data); err != nil {
		return nil, r.h.readError(err)
	}
	return data, nil
}

// readError wraps err as the *fs.PathError every source read failure is,
// naming the source by its identity rather than its capability URL.
func (h *Handle) readError(err error) error {
	return &fs.PathError{Op: opRead, Path: "source " + h.etag, Err: err}
}

// Sequential reader bounds. A sequential reader serves one pass over a
// source front to back, such as checking a keyframe index against every
// cluster: it streams large ranges and caches the pages it reads.
const (
	// sequentialWindowBytes is the most one upstream request asks for. Reads
	// stream through it, so the window costs no memory.
	sequentialWindowBytes = int64(256) << 20
	// sequentialSkipBytes is how far ahead of the stream a read may land and
	// still be reached by discarding the bytes between; one further ahead,
	// or behind, starts a new request.
	sequentialSkipBytes = int64(8) << 20
	// The deadline: readerTimeout plus the source's size at
	// sequentialMinBytesPerSecond, at most sequentialMaxTimeout. A 4 GiB
	// file gets about six minutes, a 60 GiB remux the full two hours.
	sequentialMinBytesPerSecond = 16 << 20
	sequentialMaxTimeout        = 2 * time.Hour
)

// sequentialTimeout bounds a sequential reader of a source of size bytes.
func sequentialTimeout(size int64) time.Duration {
	if size <= 0 {
		return readerTimeout
	}
	return min(readerTimeout+time.Duration(size/sequentialMinBytesPerSecond)*time.Second, sequentialMaxTimeout)
}

// SequentialReaderAt opens a reader of the handle's source for one pass
// front to back, bounded by ctx. With core access it reads through one
// streamed range at a time, up to 256 MiB, instead of a request per 64 KiB
// page, so a pass over a whole film takes a few requests rather than
// hundreds of thousands. It shares ReaderAt's process slots and byte budget,
// and its deadline grows with the source's size (sequentialTimeout). Random
// reads work too, at the cost of a new request each. With local access it is
// ReaderAt.
func (h *Handle) SequentialReaderAt(ctx context.Context) (Reader, error) {
	if !h.remote {
		return h.ReaderAt(ctx)
	}
	if err := h.Err(); err != nil {
		return nil, h.readError(err)
	}
	if err := h.acquireReaderSlot(ctx); err != nil {
		return nil, err
	}
	return &SequentialReader{pages: h.newRangeReader(ctx, sequentialTimeout(h.size))}, nil
}

// SequentialReader is SequentialReaderAt's reader of a core-access source:
// pages in RangeReader's cache, filled from one streamed range while reads
// move forward.
type SequentialReader struct {
	// pages holds the cache, bounds and slot; its fetch is unused.
	pages *RangeReader
	// body streams [pos, end) of the source, or is nil.
	body     *rangeBody
	pos, end int64
}

// Close releases the reader, its stream and its process slot. It cancels the
// reader first, so a read stalled on the stream, which holds the lock, ends
// rather than holding Close up.
func (r *SequentialReader) Close() error {
	r.pages.cancel()
	r.pages.mu.Lock()
	r.dropStream()
	r.pages.mu.Unlock()
	return r.pages.Close()
}

// ReadAt reads len(p) bytes at off, or up to the end of the source with
// io.EOF.
func (r *SequentialReader) ReadAt(p []byte, off int64) (int, error) {
	c := r.pages
	c.mu.Lock()
	defer c.mu.Unlock()
	available, err := c.readable(len(p), off)
	if available == 0 {
		return 0, err
	}
	size := c.h.size
	copied := int64(0)
	for copied < available {
		at := off + copied
		pageAt := at / readerPageBytes * readerPageBytes
		page := c.pages[pageAt]
		if page == nil {
			data, err := r.readPage(pageAt, min(readerPageBytes, size-pageAt))
			if err != nil {
				return int(copied), err
			}
			c.cache(pageAt, data)
			page = c.pages[pageAt]
		}
		c.order.MoveToFront(page)
		cached, _ := page.Value.(*readerPage)
		copied += int64(copy(p[copied:available], cached.data[at-pageAt:]))
	}
	if available < int64(len(p)) {
		return int(copied), io.EOF
	}
	return int(copied), nil
}

// readPage reads length bytes at off from the stream, discarding up to
// sequentialSkipBytes to reach it, or from a new stream starting there. Every
// byte read or discarded counts against the budget.
func (r *SequentialReader) readPage(off, length int64) ([]byte, error) {
	c := r.pages
	if r.body == nil || off < r.pos || off-r.pos > sequentialSkipBytes || off+length > r.end {
		r.dropStream()
		window := min(sequentialWindowBytes, c.h.size-off, c.maxBytes-c.fetched)
		if window < length {
			return nil, c.h.readError(ErrReaderLimit)
		}
		body, err := c.h.fetchRange(c.ctx, off, window)
		if err != nil {
			return nil, c.h.readError(err)
		}
		r.body, r.pos, r.end = body, off, off+window
	}
	if skip := off - r.pos; skip > 0 {
		if skip > c.maxBytes-c.fetched {
			return nil, c.h.readError(ErrReaderLimit)
		}
		n, err := io.CopyN(io.Discard, r.body, skip)
		c.fetched += n
		r.pos += n
		if err != nil {
			r.dropStream()
			return nil, c.h.readError(err)
		}
	}
	if length > c.maxBytes-c.fetched {
		return nil, c.h.readError(ErrReaderLimit)
	}
	data := make([]byte, length)
	n, err := io.ReadFull(r.body, data)
	c.fetched += int64(n)
	r.pos += int64(n)
	if err != nil {
		r.dropStream()
		return nil, c.h.readError(err)
	}
	return data, nil
}

func (r *SequentialReader) dropStream() {
	if r.body != nil {
		_ = r.body.Close()
		r.body = nil
	}
}

package workerinput

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"
)

var ErrReaderLimit = errors.New("admitted source reader budget exceeded")
var readerSlots = make(chan struct{}, 4)

const (
	defaultReaderCacheBytes = 2 << 20
	defaultReaderAheadBytes = 64 << 10
	defaultReaderRangeBytes = 1 << 20
	maximumReaderBytes      = int64(64) << 30
	defaultReaderRequests   = 1_000_000
	defaultReaderTimeout    = 2 * time.Minute
)

// ReaderLimits bounds one container-index operation. Fetched bytes include
// read-ahead; cache hits still check live authority. Zero fields use defaults.
type ReaderLimits struct {
	CacheBytes, ReadAheadBytes, MaxRangeBytes, MaxBytes int64
	MaxRequests                                         int
	Timeout                                             time.Duration
}

type readerPage struct {
	offset int64
	data   []byte
}

// RangeReader owns a small in-memory window, not a staged source file. Four
// readers per process bound concurrent index work; source fetches also share
// the adapter's existing reader admission with FFmpeg.
type RangeReader struct {
	handle              *Handle
	ctx                 context.Context
	cancel              context.CancelFunc
	stop                func() bool
	limits              ReaderLimits
	mu                  sync.Mutex
	once                sync.Once
	pages               map[int64]*list.Element
	order               *list.List
	cacheBytes, fetched int64
	requests            int
}

// Validate checks the admitted handle without a control or metadata RPC.
func (h *Handle) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h == nil || h.adapter == nil || !h.adapter.authorized(h) {
		return errors.New("source authority unavailable")
	}
	return nil
}

// Context is the source handle's lifetime, canceled when its work is released.
func (h *Handle) Context() context.Context { return h.ctx }

func (h *Handle) ReaderAt(ctx context.Context, limits ReaderLimits) (*RangeReader, error) {
	if err := h.Validate(ctx); err != nil {
		return nil, sourceReadError(err)
	}
	if limits.CacheBytes == 0 {
		limits.CacheBytes = defaultReaderCacheBytes
	}
	if limits.ReadAheadBytes == 0 {
		limits.ReadAheadBytes = defaultReaderAheadBytes
	}
	if limits.MaxRangeBytes == 0 {
		limits.MaxRangeBytes = defaultReaderRangeBytes
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = min(maximumReaderBytes, h.source.Size*2)
	}
	if limits.MaxRequests == 0 {
		limits.MaxRequests = defaultReaderRequests
	}
	if limits.Timeout == 0 {
		limits.Timeout = defaultReaderTimeout
	}
	if limits.CacheBytes < 1 || limits.CacheBytes > 8<<20 || limits.ReadAheadBytes < 1 || limits.ReadAheadBytes > 1<<20 || limits.MaxRangeBytes < limits.ReadAheadBytes || limits.MaxRangeBytes > 4<<20 || limits.MaxBytes < 1 || limits.MaxBytes > maximumReaderBytes || limits.MaxRequests < 1 || limits.MaxRequests > defaultReaderRequests || limits.Timeout <= 0 || limits.Timeout > defaultReaderTimeout {
		return nil, sourceReadError(ErrReaderLimit)
	}
	select {
	case readerSlots <- struct{}{}:
	default:
		return nil, sourceReadError(ErrReaderLimit)
	}
	bounded, cancel := context.WithTimeout(ctx, limits.Timeout)
	r := &RangeReader{handle: h, ctx: bounded, cancel: cancel, limits: limits, pages: map[int64]*list.Element{}, order: list.New()}
	r.stop = context.AfterFunc(h.ctx, cancel)
	return r, nil
}
func (r *RangeReader) Close() error {
	r.once.Do(func() {
		r.cancel()
		r.stop()
		r.mu.Lock()
		r.pages = nil
		r.order.Init()
		r.cacheBytes = 0
		r.mu.Unlock()
		<-readerSlots
	})
	return nil
}
func sourceReadError(err error) error {
	return &fs.PathError{Op: "read", Path: "admitted source", Err: err}
}

func (r *RangeReader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.handle.Validate(r.ctx); err != nil {
		return 0, sourceReadError(err)
	}
	if off < 0 {
		return 0, sourceReadError(errors.New("negative source offset"))
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.handle.Size() {
		return 0, io.EOF
	}
	available := min(int64(len(p)), r.handle.Size()-off)
	copied := int64(0)
	for copied < available {
		if err := r.handle.Validate(r.ctx); err != nil {
			return int(copied), sourceReadError(err)
		}
		at := off + copied
		pageAt := (at / r.limits.ReadAheadBytes) * r.limits.ReadAheadBytes
		if page := r.pages[pageAt]; page != nil {
			r.order.MoveToFront(page)
			cached, _ := page.Value.(*readerPage)
			n := copy(p[copied:available], cached.data[at-pageAt:])
			copied += int64(n)
			continue
		}
		// Large metadata reads use coalesced ranges; tiny block-header reads use
		// aligned read-ahead so nearby headers share one request.
		start := pageAt
		length := min(r.limits.ReadAheadBytes, r.handle.Size()-start)
		direct := available-copied > r.limits.ReadAheadBytes
		if direct {
			start = at
			length = min(available-copied, r.limits.MaxRangeBytes)
		}
		data, err := r.fetch(start, length)
		if err != nil {
			return int(copied), err
		}
		if direct {
			n := copy(p[copied:available], data)
			copied += int64(n)
			continue
		}
		r.cache(start, data)
		n := copy(p[copied:available], data[at-start:])
		copied += int64(n)
	}
	if err := r.handle.Validate(r.ctx); err != nil {
		return 0, sourceReadError(err)
	}
	if available < int64(len(p)) {
		return int(copied), io.EOF
	}
	return int(copied), nil
}
func (r *RangeReader) cache(offset int64, data []byte) {
	if int64(len(data)) > r.limits.CacheBytes {
		return
	}
	r.pages[offset] = r.order.PushFront(&readerPage{offset: offset, data: data})
	r.cacheBytes += int64(len(data))
	for r.cacheBytes > r.limits.CacheBytes {
		last := r.order.Back()
		page, _ := last.Value.(*readerPage)
		delete(r.pages, page.offset)
		r.cacheBytes -= int64(len(page.data))
		r.order.Remove(last)
	}
}
func (r *RangeReader) fetch(off, length int64) ([]byte, error) {
	if r.requests >= r.limits.MaxRequests || length > r.limits.MaxBytes-r.fetched {
		return nil, sourceReadError(ErrReaderLimit)
	}
	r.requests++
	r.fetched += length
	a := r.handle.adapter
	select {
	case a.reads <- struct{}{}:
		defer func() { <-a.reads }()
	case <-r.ctx.Done():
		return nil, sourceReadError(r.ctx.Err())
	}
	if err := r.handle.Validate(r.ctx); err != nil {
		return nil, sourceReadError(err)
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.handle.source.URL, nil)
	if err != nil {
		return nil, sourceReadError(errors.New("invalid source request"))
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+length-1))
	req.Header.Set("X-Silo-Source-Grant", r.handle.source.Grant)
	req.Header.Set("If-Match", r.handle.source.ETag)
	req.Header.Set("Accept-Encoding", "identity")
	if a.options.Sign != nil {
		if err := a.options.Sign(req); err != nil {
			return nil, sourceReadError(errors.New("source proof unavailable"))
		}
	}
	response, err := a.client.Do(req)
	if err != nil {
		if r.ctx.Err() != nil {
			return nil, sourceReadError(r.ctx.Err())
		}
		return nil, sourceReadError(errors.New("source request unavailable"))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusGone || response.StatusCode == http.StatusPreconditionFailed {
		_ = r.handle.Close()
		return nil, sourceReadError(errors.New("source authority changed"))
	}
	if response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusTooManyRequests {
		return nil, sourceReadError(errors.New("source temporarily unavailable"))
	}
	if _, err := validateResponse(req, response, r.handle.source); err != nil {
		_ = r.handle.Close()
		return nil, sourceReadError(errors.New("invalid source range representation"))
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(response.Body, data); err != nil {
		if r.ctx.Err() != nil {
			return nil, sourceReadError(r.ctx.Err())
		}
		_ = r.handle.Close()
		return nil, sourceReadError(errors.New("incomplete source range representation"))
	}
	if err := r.handle.Validate(r.ctx); err != nil {
		return nil, sourceReadError(err)
	}
	return data, nil
}

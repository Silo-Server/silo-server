package nodesource

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Core source access: the loopback relay.
//
// A core-access source is served to FFmpeg, and to the node's Go readers,
// at a loopback URL, http://127.0.0.1:<port>/src/<32 hex>. The URL is the
// job's input path, so it is one per source revision in this process: every
// job that reads a revision gets the same URL, and the caches main keys by
// input path (keyframe indexes and their rejections, subtitle indexes and cue
// windows, staged burn-in reads, the DV RPU probe, copy-seek anchors, the
// automatic transcode fallback) hit across sessions as they do on a library
// path. The jobs share one relay entry, reference-counted, with one grant
// renewal loop; the URL resolves while at least one of them holds it. Its
// token is an HMAC of the revision under a key drawn when the process
// starts, so it is unguessable, and it is a bearer capability on the node
// host: whoever can reach the loopback listener with it reads the source
// while it is open. It never reaches a log (redact.go).

// relayURLPattern matches a relay URL.
var relayURLPattern = regexp.MustCompile(`http://127\.0\.0\.1:[0-9]{1,5}/src/[0-9a-f]{32}`)

// relayURLPrefix is what every relay URL starts with, for a cheap test before
// the pattern.
const relayURLPrefix = "http://127.0.0.1:"

// IsRelayURL reports whether input is a relay URL, whether or not its source
// is still open: a capability, never a local path.
func IsRelayURL(input string) bool {
	if !strings.HasPrefix(input, relayURLPrefix) {
		return false
	}
	loc := relayURLPattern.FindStringIndex(input)
	return loc != nil && loc[0] == 0 && loc[1] == len(input)
}

// registry finds the open relay entry behind a relay URL, for the fspath
// hooks (registry.go), which receive FFmpeg's input string.
var registry = struct {
	sync.RWMutex
	byURL map[string]*relayEntry
}{byURL: make(map[string]*relayEntry)}

func lookupRelay(url string) *relayEntry {
	registry.RLock()
	defer registry.RUnlock()
	return registry.byURL[url]
}

// relayListener is an opener's loopback listener, started by its first
// core-access source and kept until the opener closes, so a revision's URL
// stays the same for the life of the process.
type relayListener struct {
	tokenKey []byte

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	closed   bool
}

func newRelayListener() relayListener {
	key := make([]byte, 32)
	_, _ = rand.Read(key) // crypto/rand never fails on supported platforms
	return relayListener{tokenKey: key}
}

// listenLoopback opens the relay's listener; tests replace it.
var listenLoopback = func() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }

// addr starts the listener on first use and returns its address. A failure
// is not remembered: the next core-access source tries again.
func (r *relayListener) addr(handler http.Handler) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", ErrOpenerClosed
	}
	if r.listener != nil {
		return r.listener.Addr().String(), nil
	}
	listener, err := listenLoopback()
	if err != nil {
		return "", fmt.Errorf("nodesource: listen on loopback: %w", err)
	}
	r.listener = listener
	r.server = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// The relay logs nothing a client caused: its URLs are capabilities.
		ErrorLog: slog.NewLogLogger(slog.DiscardHandler, slog.LevelError),
	}
	go func(server *http.Server) { _ = server.Serve(listener) }(r.server)
	return listener.Addr().String(), nil
}

func (r *relayListener) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.server != nil {
		return r.server.Close()
	}
	return nil
}

// token is the URL token of a revision: an HMAC of its path and entity tag
// under the process's key.
func (r *relayListener) token(path, etag string) string {
	mac := hmac.New(sha256.New, r.tokenKey)
	_, _ = io.WriteString(mac, path+"\x00"+etag)
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// relayEntry is one source revision served on the relay: its lease, its URL
// and its page cache, shared by every job that reads it.
type relayEntry struct {
	*lease
	token string
	url   string
	pages *pageCache
	// refs is the number of open sources on the entry, under opener.mu.
	refs int
}

// openCore returns a core-access source for opened: a reference to the relay
// entry of its revision, created if no job holds one now.
func (o *Opener) openCore(opened *OpenResponse) (Source, error) {
	addr, err := o.relay.addr(http.HandlerFunc(o.serve))
	if err != nil {
		return nil, err
	}
	token := o.relay.token(opened.Path, opened.ETag)
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, ErrOpenerClosed
	}
	e := o.entries[token]
	// An entry its last holder released is ending: it is replaced, not
	// revived.
	if e == nil || e.refs <= 0 || e.ctx.Err() != nil {
		e = &relayEntry{lease: o.newLease(opened), token: token, url: "http://" + addr + "/src/" + token}
		e.pages = newPageCache(e)
		e.release = func() { o.dropEntry(e) }
		o.entries[token] = e
		registry.Lock()
		registry.byURL[e.url] = e
		registry.Unlock()
		o.trackLocked(e.lease)
	} else {
		e.offerGrant(opened.Grant)
	}
	e.refs++
	o.mu.Unlock()
	return newCoreHandle(e), nil
}

// dropEntry forgets an ended entry: its URL stops resolving.
func (o *Opener) dropEntry(e *relayEntry) {
	o.mu.Lock()
	delete(o.leases, e.lease)
	if o.entries[e.token] == e {
		delete(o.entries, e.token)
	}
	o.mu.Unlock()
	registry.Lock()
	if registry.byURL[e.url] == e {
		delete(registry.byURL, e.url)
	}
	registry.Unlock()
}

// unref releases one source's hold on e, and ends e with the last.
func (o *Opener) unref(e *relayEntry) {
	o.mu.Lock()
	e.refs--
	last := e.refs <= 0
	o.mu.Unlock()
	if last {
		e.end(ErrClosed)
	}
}

// liveEntry is the open entry of token, or nil.
func (o *Opener) liveEntry(token string) *relayEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	if e := o.entries[token]; e != nil && e.refs > 0 && e.ctx.Err() == nil {
		return e
	}
	return nil
}

// coreHandle is a core-access Source: one job's hold on a relay entry. It
// ends when the job closes it or the entry ends.
type coreHandle struct {
	e      *relayEntry
	ctx    context.Context //nolint:containedctx // the source's own lifetime, which Context exposes
	cancel context.CancelCauseFunc
	stop   func() bool
	closed atomic.Bool
}

func newCoreHandle(e *relayEntry) *coreHandle {
	ctx, cancel := context.WithCancelCause(context.Background())
	h := &coreHandle{e: e, ctx: ctx, cancel: cancel}
	h.stop = context.AfterFunc(e.ctx, func() { cancel(context.Cause(e.ctx)) })
	return h
}

func (h *coreHandle) Input() string            { return h.e.url }
func (h *coreHandle) Identity() string         { return h.e.etag }
func (h *coreHandle) Size() int64              { return h.e.size }
func (h *coreHandle) ModTime() time.Time       { return h.e.modTime }
func (h *coreHandle) Context() context.Context { return h.ctx }
func (h *coreHandle) Err() error               { return context.Cause(h.ctx) }

// Close releases the job's hold. The last hold ends the entry, which stops
// its renewal, unregisters its URL and interrupts every read in flight,
// including a write FFmpeg's back pressure holds.
func (h *coreHandle) Close() error {
	if h.closed.CompareAndSwap(false, true) {
		h.stop()
		h.cancel(ErrClosed)
		h.e.opener.unref(h.e)
	}
	return nil
}

// serve answers FFmpeg on the loopback listener for one entry's URL. It
// forwards a single byte range on GET, always advertises ranges, and sends an
// exact Content-Length, with Content-Range on 206. HEAD is answered from the
// entry's revision. A transient upstream failure mid-body resumes while
// FFmpeg's request lives; a body that cannot be completed is aborted so
// FFmpeg sees the truncation instead of a short success.
func (o *Opener) serve(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := strings.CutPrefix(req.URL.Path, "/src/")
	var e *relayEntry
	if ok {
		e = o.liveEntry(token)
	}
	if e == nil {
		http.NotFound(w, req)
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	start, length, partial := int64(0), e.size, false
	if req.Method == http.MethodGet {
		var satisfiable bool
		start, length, partial, satisfiable = parseRange(req.Header.Get("Range"), e.size)
		if !satisfiable {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", e.size))
			http.Error(w, http.StatusText(http.StatusRequestedRangeNotSatisfiable), http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}
	header := w.Header()
	header.Set("Accept-Ranges", "bytes")
	header.Set("Content-Type", "application/octet-stream")
	header.Set("ETag", e.etag)
	if req.Method == http.MethodHead {
		header.Set("Content-Length", strconv.FormatInt(e.size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	body, err := e.fetchRange(req.Context(), start, length)
	if err != nil {
		if req.Context().Err() == nil {
			refuseRelayRead(w, e, err)
		}
		return
	}
	defer func() { _ = body.Close() }()
	// An ended entry interrupts the body even while FFmpeg's back pressure
	// holds a write. The upstream read already ends with the entry.
	controller := http.NewResponseController(w)
	interrupted := make(chan struct{})
	stopInterrupt := context.AfterFunc(e.ctx, func() {
		_ = controller.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		if !stopInterrupt() {
			<-interrupted
		}
	}()
	header.Set("Content-Length", strconv.FormatInt(length, 10))
	status := http.StatusOK
	if partial {
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, e.size))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	// Flush every write: FFmpeg probing a slow source waits for the first
	// bytes, not for a full buffer.
	_ = controller.Flush()
	buffer := make([]byte, 64<<10)
	for written := int64(0); written < length; {
		n, readErr := body.Read(buffer)
		if n > 0 {
			if _, err := w.Write(buffer[:n]); err != nil {
				if req.Context().Err() != nil {
					return
				}
				// The write failed while FFmpeg's request lives (the entry
				// ended, for one). Abort, so the cut body is not finished
				// as if it were whole.
				panic(http.ErrAbortHandler)
			}
			_ = controller.Flush()
			written += int64(n)
		}
		if readErr != nil {
			if written == length && errors.Is(readErr, io.EOF) {
				return
			}
			if req.Context().Err() != nil {
				return
			}
			// The body cannot be completed. Abort the connection rather
			// than end it cleanly, so FFmpeg sees a short read.
			panic(http.ErrAbortHandler)
		}
	}
}

// refuseRelayRead answers FFmpeg's read that could not start: the core's
// fatal status or 416 as it is, an ended entry's status, else 502.
func refuseRelayRead(w http.ResponseWriter, e *relayEntry, err error) {
	status := http.StatusBadGateway
	if statusErr, ok := errors.AsType[*StatusError](err); ok && (statusErr.Fatal() || statusErr.Status == http.StatusRequestedRangeNotSatisfiable) {
		status = statusErr.Status
	} else if entryErr := e.Err(); entryErr != nil {
		status = endedStatus(entryErr)
	}
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", e.size))
	}
	w.Header().Del("ETag")
	http.Error(w, http.StatusText(status), status)
}

// endedStatus is the status an ended entry answers FFmpeg with.
func endedStatus(err error) int {
	if statusErr, ok := errors.AsType[*StatusError](err); ok && statusErr.Fatal() {
		return statusErr.Status
	}
	return http.StatusGone
}

// parseRange reads a single-range Range header against size. A missing or
// unsupported header (several ranges, another unit, garbage) selects the
// whole source, as RFC 9110 lets a server ignore Range. satisfiable is false
// for a range that starts past the source.
func parseRange(header string, size int64) (start, length int64, partial, satisfiable bool) {
	value, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !ok || strings.Contains(value, ",") {
		return 0, size, false, true
	}
	first, last, ok := strings.Cut(strings.TrimSpace(value), "-")
	if !ok {
		return 0, size, false, true
	}
	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n < 0 {
			return 0, size, false, true
		}
		if n == 0 || size == 0 {
			return 0, 0, false, false
		}
		n = min(n, size)
		return size - n, n, true, true
	}
	from, err := strconv.ParseInt(first, 10, 64)
	if err != nil || from < 0 {
		return 0, size, false, true
	}
	if from >= size {
		return 0, 0, false, false
	}
	to := size - 1
	if last != "" {
		to, err = strconv.ParseInt(last, 10, 64)
		if err != nil || to < from {
			return 0, size, false, true
		}
		to = min(to, size-1)
	}
	return from, to - from + 1, true, true
}

package nodesource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
)

// backoff paces upstream retries: the first waits initial, each later one
// twice the last up to max, and a read gives up once it has waited giveUp on
// the core without a byte arriving. Only time spent waiting on the core
// counts: a caller that pauses between reads (FFmpeg's throttle, client back
// pressure) does not use up the window. One wait on the core (a request, its
// answer, one body read) that makes no progress for stall is abandoned and
// retried, and a response body left unread for idle is dropped, so the next
// read resumes with a fresh request rather than on a connection the core may
// have cut meanwhile.
type backoff struct {
	initial, max, giveUp time.Duration
	stall, idle          time.Duration
}

var defaultBackoff = backoff{
	initial: 250 * time.Millisecond, max: 4 * time.Second, giveUp: 30 * time.Second,
	stall: 10 * time.Second, idle: 10 * time.Second,
}

// next is the wait after one that lasted last.
func (b backoff) next(last time.Duration) time.Duration {
	return min(max(last*2, b.initial), b.max)
}

// errUpstreamStalled is a fetch that made no progress for the whole retry
// window.
var errUpstreamStalled = errors.New("nodesource: the core sent no source bytes within the retry window")

// errRangeNotSatisfiable is the core's 416: the range lies past the source.
var errRangeNotSatisfiable = &StatusError{Op: opRead, Status: http.StatusRequestedRangeNotSatisfiable}

// fetchRange returns bytes [off, off+n) of the handle's source, read from the
// core. It is the one upstream primitive: the FFmpeg relay and the ReaderAt
// both read through it. Each request is signed, conditional on the revision's
// ETag, and its response checked for that ETag, an identity encoding and the
// exact range and length. A network error, 5xx, 429, invalid response or
// stalled wait is transient: the read re-requests from where it stopped,
// backing off, and fails only when it has waited the retry window on the core
// without progress, or ctx ends. A fatal status (403, 404, 410, 412) fails the
// handle and the read. A 416 fails only the read.
func (h *Handle) fetchRange(ctx context.Context, off, n int64) (*rangeBody, error) {
	if off < 0 || n < 0 || off+n > h.size {
		return nil, errRangeNotSatisfiable
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.ctx, cancel)
	now := h.relay.now()
	b := &rangeBody{h: h, ctx: ctx, cancel: cancel, stop: stop, off: off, end: off + n, activeSince: now, lastRead: now}
	if n > 0 {
		err := b.connect()
		b.lastRead = h.relay.now()
		b.waited = b.lastRead.Sub(b.activeSince)
		if err != nil {
			_ = b.Close()
			return nil, err
		}
	}
	return b, nil
}

// rangeBody is one fetchRange read. Read resumes after a transient failure
// while the read's context lives.
type rangeBody struct {
	h      *Handle
	ctx    context.Context
	cancel context.CancelFunc
	stop   func() bool
	// off is the next byte to read, end the first byte after the range.
	off, end int64
	// body is the current upstream answer, and endAttempt cancels the
	// request it answers.
	body       io.ReadCloser
	endAttempt context.CancelFunc
	delay      time.Duration
	// waited is the time spent waiting on the core since the last byte
	// arrived, up to the start of the current Read (activeSince). lastRead is
	// when the last Read returned.
	waited      time.Duration
	activeSince time.Time
	lastRead    time.Time
}

func (b *rangeBody) Read(p []byte) (int, error) {
	now := b.h.relay.now()
	if b.body != nil && now.Sub(b.lastRead) > b.h.relay.retry.idle {
		// The caller paused long enough for the core's write deadline to
		// have dropped the connection, or for it to have died unnoticed.
		// Resume with a fresh request instead of waiting on it.
		b.dropResponse()
	}
	b.activeSince = now
	n, err := b.read(p)
	b.lastRead = b.h.relay.now()
	if n > 0 {
		b.waited = 0
	} else {
		b.waited += b.lastRead.Sub(b.activeSince)
	}
	return n, err
}

func (b *rangeBody) read(p []byte) (int, error) {
	for {
		if b.off >= b.end {
			return 0, io.EOF
		}
		if b.body == nil {
			if err := b.connect(); err != nil {
				return 0, err
			}
		}
		n, err := b.readBody(p[:min(int64(len(p)), b.end-b.off)])
		if n > 0 {
			b.off += int64(n)
			b.delay = 0
		}
		if err != nil || b.off >= b.end {
			// The upstream body ended, broke or stalled. Anything still
			// missing is re-requested on the next Read, so a cut connection
			// resumes from b.off.
			b.dropResponse()
		}
		if n > 0 {
			return n, nil
		}
		if err := b.ctx.Err(); err != nil {
			return 0, b.cause()
		}
		if err != nil {
			// An answer that ended without a byte counts as a failed
			// attempt, so a core that keeps doing that is backed off from
			// and eventually given up on.
			b.h.relay.logger.Debug("source read failed; resuming", "source", b.h.etag, "offset", b.off, "error", err)
			b.delay = b.h.relay.retry.next(b.delay)
		}
	}
}

// readBody reads the current answer once, abandoning the attempt when no
// byte arrives within the stall timeout.
func (b *rangeBody) readBody(p []byte) (int, error) {
	timer := time.AfterFunc(b.h.relay.retry.stall, b.endAttempt)
	n, err := b.body.Read(p)
	if !timer.Stop() && n == 0 && err != nil && b.ctx.Err() == nil {
		err = fmt.Errorf("nodesource: no source bytes for %s: %w", b.h.relay.retry.stall, err)
	}
	return n, err
}

// stalledFor is the time spent waiting on the core since the last byte
// arrived, not counting pauses between Reads.
func (b *rangeBody) stalledFor() time.Duration {
	return b.waited + b.h.relay.now().Sub(b.activeSince)
}

// Close ends the read and releases its connection.
func (b *rangeBody) Close() error {
	b.dropResponse()
	b.stop()
	b.cancel()
	return nil
}

func (b *rangeBody) dropResponse() {
	if b.body != nil {
		_ = b.body.Close()
		b.body = nil
	}
	if b.endAttempt != nil {
		b.endAttempt()
		b.endAttempt = nil
	}
}

// cause is why the read's context ended: the handle's failure or close when
// it ended first, else the caller's cancellation.
func (b *rangeBody) cause() error {
	if err := b.h.Err(); err != nil {
		return err
	}
	return b.ctx.Err()
}

// connect requests [b.off, b.end), waiting out transient failures.
func (b *rangeBody) connect() error {
	policy := b.h.relay.retry
	for {
		if b.delay > 0 {
			if b.stalledFor()+b.delay > policy.giveUp {
				return errUpstreamStalled
			}
			if !sleepCtx(b.ctx, b.delay) {
				return b.cause()
			}
		}
		err := b.request()
		if err == nil {
			return nil
		}
		if b.ctx.Err() != nil {
			return b.cause()
		}
		var statusErr *StatusError
		if errors.As(err, &statusErr) {
			if statusErr.Fatal() {
				b.h.fail(statusErr)
				return statusErr
			}
			if statusErr.Status == http.StatusRequestedRangeNotSatisfiable {
				return statusErr
			}
		}
		b.h.relay.logger.Debug("source read failed; retrying", "source", b.h.etag, "offset", b.off, "error", err)
		b.delay = policy.next(b.delay)
	}
}

// request sends one signed range request, checks its answer and makes its
// body the current one. An attempt that gets no answer within the stall
// timeout, including reading the body of a refusal, is abandoned.
func (b *rangeBody) request() error {
	ctx, cancel := context.WithCancel(b.ctx)
	timer := time.AfterFunc(b.h.relay.retry.stall, cancel)
	body, err := b.send(ctx)
	if !timer.Stop() && err != nil && b.ctx.Err() == nil {
		err = fmt.Errorf("nodesource: no source answer within %s: %w", b.h.relay.retry.stall, err)
	}
	if err != nil {
		cancel()
		return err
	}
	b.body, b.endAttempt = body, cancel
	return nil
}

func (b *rangeBody) send(ctx context.Context) (io.ReadCloser, error) {
	r := b.h.relay
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.coreURL+PathSources, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(HeaderSourceGrant, b.h.currentGrant())
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", b.off, b.end-1))
	req.Header.Set("If-Match", b.h.etag)
	req.Header.Set("Accept-Encoding", "identity")
	if err := nodeauth.SignToCore(req, r.coreURL, r.nodeID, r.key); err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPartialContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, &StatusError{Op: opRead, Status: resp.StatusCode}
	}
	if err := b.validate(resp); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

// validate checks that resp is exactly the range asked for, of the pinned
// revision, unencoded.
func (b *rangeBody) validate(resp *http.Response) error {
	if resp.Header.Get("ETag") != b.h.etag {
		return errors.New("nodesource: source answer is another revision")
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return errors.New("nodesource: source answer is encoded")
	}
	want := fmt.Sprintf("bytes %d-%d/%d", b.off, b.end-1, b.h.size)
	if resp.Header.Get("Content-Range") != want || resp.ContentLength != b.end-b.off {
		return errors.New("nodesource: source answer is not the requested range")
	}
	return nil
}

// serve answers FFmpeg on the loopback listener for one handle's URL. It
// forwards a single byte range on GET, always advertises ranges, and sends an
// exact Content-Length, with Content-Range on 206. HEAD is answered from the
// handle's metadata. A transient upstream failure mid-body resumes while
// FFmpeg's request lives; a body that cannot be completed is aborted so
// FFmpeg sees the truncation instead of a short success.
func (r *Relay) serve(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := strings.CutPrefix(req.URL.Path, "/src/")
	r.mu.Lock()
	h := r.handles[token]
	r.mu.Unlock()
	if !ok || h == nil {
		http.NotFound(w, req)
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if err := h.Err(); err != nil {
		status := handleStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	start, length, partial := int64(0), h.size, false
	if req.Method == http.MethodGet {
		var satisfiable bool
		start, length, partial, satisfiable = parseRange(req.Header.Get("Range"), h.size)
		if !satisfiable {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", h.size))
			http.Error(w, http.StatusText(http.StatusRequestedRangeNotSatisfiable), http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}
	header := w.Header()
	header.Set("Accept-Ranges", "bytes")
	header.Set("Content-Type", "application/octet-stream")
	header.Set("ETag", h.etag)
	if req.Method == http.MethodHead {
		header.Set("Content-Length", strconv.FormatInt(h.size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	body, err := h.fetchRange(req.Context(), start, length)
	if err != nil {
		if req.Context().Err() != nil {
			return
		}
		status := http.StatusBadGateway
		var statusErr *StatusError
		if errors.As(err, &statusErr) && (statusErr.Fatal() || statusErr.Status == http.StatusRequestedRangeNotSatisfiable) {
			status = statusErr.Status
			if status == http.StatusRequestedRangeNotSatisfiable {
				header.Set("Content-Range", fmt.Sprintf("bytes */%d", h.size))
			}
		} else if handleErr := h.Err(); handleErr != nil {
			status = handleStatus(handleErr)
		}
		header.Del("ETag")
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer func() { _ = body.Close() }()
	// A closed or failed handle interrupts the body even while FFmpeg's back
	// pressure holds a write. The upstream read already ends with the handle.
	interrupted := make(chan struct{})
	stopInterrupt := context.AfterFunc(h.ctx, func() {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now())
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
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, h.size))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	// Flush every write: FFmpeg probing a slow source waits for the first
	// bytes, not for a full buffer.
	flusher := http.NewResponseController(w)
	_ = flusher.Flush()
	buffer := make([]byte, 64<<10)
	for written := int64(0); written < length; {
		n, readErr := body.Read(buffer)
		if n > 0 {
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			_ = flusher.Flush()
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

// handleStatus is the status a failed or closed handle answers FFmpeg with.
func handleStatus(err error) int {
	var statusErr *StatusError
	if errors.As(err, &statusErr) && statusErr.Fatal() {
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

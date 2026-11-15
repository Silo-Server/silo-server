package nodesource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// backoff paces upstream reads: the first retry waits initial, each later
// one twice the last up to max, and a read gives up once it has waited giveUp
// on the core without a byte arriving. Only time spent waiting on the core
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

// defaultBackoff gives up after 30 seconds without progress, so a core that is
// down for less resumes the read and one that is down for longer fails the
// job's read, as a lost network mount does.
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

// opRead names a source read in errors.
const opRead = "read"

// errRangeNotSatisfiable is the core's 416: the range lies past the source.
var errRangeNotSatisfiable = &StatusError{Op: opRead, Status: http.StatusRequestedRangeNotSatisfiable}

// fetchRange returns bytes [off, off+n) of the entry's source, read from the
// core. It is the one upstream primitive: the FFmpeg relay and the readers
// both read through it. Each request is signed, conditional on the
// revision's ETag, and its answer checked for that ETag, an identity encoding
// and the exact range and length. A network error, 5xx, 429, 401, invalid
// answer or stalled wait is transient: the read re-requests from where it
// stopped, backing off, and fails only when it has waited the retry window on
// the core without progress, or ctx or the entry ends. A fatal status (400,
// 403, 404, 410, 412) fails the entry, and so every job reading it, and the
// read. A 416 fails only the read.
func (e *relayEntry) fetchRange(ctx context.Context, off, n int64) (*rangeBody, error) {
	if off < 0 || n < 0 || off+n > e.size {
		return nil, errRangeNotSatisfiable
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.ctx, cancel)
	now := e.opener.now()
	b := &rangeBody{e: e, ctx: ctx, cancel: cancel, stop: stop, off: off, end: off + n, activeSince: now, lastRead: now}
	if n > 0 {
		err := b.connect()
		b.lastRead = e.opener.now()
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
	e      *relayEntry
	ctx    context.Context //nolint:containedctx // the read's own lifetime
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

func (b *rangeBody) policy() backoff { return b.e.opener.retry }

func (b *rangeBody) Read(p []byte) (int, error) {
	now := b.e.opener.now()
	if b.body != nil && now.Sub(b.lastRead) > b.policy().idle {
		// The caller paused long enough for the core's write deadline to
		// have dropped the connection, or for it to have died unnoticed.
		// Resume with a fresh request instead of waiting on it.
		b.dropResponse()
	}
	b.activeSince = now
	n, err := b.read(p)
	b.lastRead = b.e.opener.now()
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
			b.e.opener.logger.Debug("source read failed; resuming", "source", b.e.etag, "offset", b.off, "error", err)
			b.delay = b.policy().next(b.delay)
		}
	}
}

// readBody reads the current answer once, abandoning the attempt when no
// byte arrives within the stall timeout.
func (b *rangeBody) readBody(p []byte) (int, error) {
	timer := time.AfterFunc(b.policy().stall, b.endAttempt)
	n, err := b.body.Read(p)
	if !timer.Stop() && n == 0 && err != nil && b.ctx.Err() == nil {
		err = fmt.Errorf("nodesource: no source bytes for %s: %w", b.policy().stall, err)
	}
	return n, err
}

// stalledFor is the time spent waiting on the core since the last byte
// arrived, not counting pauses between Reads.
func (b *rangeBody) stalledFor() time.Duration {
	return b.waited + b.e.opener.now().Sub(b.activeSince)
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

// cause is why the read's context ended: the entry's failure or close when
// it ended first, else the caller's cancellation.
func (b *rangeBody) cause() error {
	if err := b.e.Err(); err != nil {
		return err
	}
	return b.ctx.Err()
}

// connect requests [b.off, b.end), waiting out transient failures.
func (b *rangeBody) connect() error {
	policy := b.policy()
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
		if statusErr, ok := errors.AsType[*StatusError](err); ok {
			if statusErr.Fatal() {
				b.e.fail(statusErr)
				return statusErr
			}
			if statusErr.Status == http.StatusRequestedRangeNotSatisfiable {
				return statusErr
			}
		}
		b.e.opener.logger.Debug("source read failed; retrying", "source", b.e.etag, "offset", b.off, "error", err)
		b.delay = policy.next(b.delay)
	}
}

// request sends one signed range request, checks its answer and makes its
// body the current one. An attempt that gets no answer within the stall
// timeout, including reading the body of a refusal, is abandoned.
func (b *rangeBody) request() error {
	ctx, cancel := context.WithCancel(b.ctx)
	timer := time.AfterFunc(b.policy().stall, cancel)
	body, err := b.send(ctx)
	if !timer.Stop() && err != nil && b.ctx.Err() == nil {
		err = fmt.Errorf("nodesource: no source answer within %s: %w", b.policy().stall, err)
	}
	if err != nil {
		cancel()
		return err
	}
	b.body, b.endAttempt = body, cancel
	return nil
}

func (b *rangeBody) send(ctx context.Context) (io.ReadCloser, error) {
	grant, _ := b.e.currentGrant()
	resp, err := b.e.opener.fetch(ctx, ReadRequest{Grant: grant, ETag: b.e.etag, Offset: b.off, Length: b.end - b.off})
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
	if resp.Header.Get("ETag") != b.e.etag {
		return errors.New("nodesource: source answer is another revision")
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return errors.New("nodesource: source answer is encoded")
	}
	want := fmt.Sprintf("bytes %d-%d/%d", b.off, b.end-1, b.e.size)
	if resp.Header.Get("Content-Range") != want || resp.ContentLength != b.end-b.off {
		return errors.New("nodesource: source answer is not the requested range")
	}
	return nil
}

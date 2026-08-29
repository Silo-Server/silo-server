// Package httpstream provides helpers for HTTP handlers that stream large or
// long-lived response bodies (direct play, remux, downloads).
//
// The main API server sets an absolute WriteTimeout, which kills any response
// still being written when the deadline elapses — including perfectly healthy
// multi-gigabyte media streams. RollingDeadlineWriter replaces that contract
// for streaming responses only: the connection's write deadline is pushed
// forward on every successful write, so a response that keeps making progress
// lives indefinitely while a stalled one is still reaped within the window.
package httpstream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	// DefaultStallWindow is how long a streaming response may go without
	// forward progress before its connection is reaped.
	DefaultStallWindow = 180 * time.Second

	// stallWindowEnv overrides DefaultStallWindow (integer seconds).
	stallWindowEnv = "SILO_STREAM_WRITE_STALL_TIMEOUT"

	// bumpStep rate-limits deadline updates so a busy stream issues one
	// SetWriteDeadline per step rather than one per 32 KB chunk. It applies to
	// Write only: a ReadFrom slice is already bounded at readFromChunk, so
	// bumping around one costs at most a syscall per 4 MiB and the throttle
	// would only shorten the window a slice runs against.
	bumpStep = 15 * time.Second

	// readFromChunk bounds each ReadFrom slice so the deadline keeps rolling.
	// At the default 180s window, 4 MiB permits steady clients down to roughly
	// 186 kbit/s without expiring mid-slice. That figure is only true because
	// every slice starts against a freshly set deadline — see forceBump.
	readFromChunk int64 = ReadFromChunkDefault
)

// StreamOutcome classifies how a streaming response ended.
type StreamOutcome string

const (
	OutcomeCompleted   StreamOutcome = "completed"
	OutcomeStalledReap StreamOutcome = "stalled_reap"
	OutcomeClientGone  StreamOutcome = "client_gone"
)

// StallWindow returns the configured stall window for streaming responses.
func StallWindow() time.Duration {
	if v := os.Getenv(stallWindowEnv); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return DefaultStallWindow
}

// RollingDeadlineWriter wraps a streaming response and rolls the connection's
// write deadline forward as the body makes progress. Construct with
// NewRollingDeadlineWriter and use in place of the original ResponseWriter.
//
// If the underlying transport does not support per-response write deadlines
// (SetWriteDeadline errors), the wrapper degrades to a plain pass-through and
// the server-level WriteTimeout, if any, stays in effect.
type RollingDeadlineWriter struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	window   time.Duration
	step     time.Duration
	lastBump time.Time
	disabled bool

	statusCode    int
	bytesWritten  int64
	firstWriteErr error
}

// NewRollingDeadlineWriter wraps w with the configured stall window.
func NewRollingDeadlineWriter(w http.ResponseWriter) *RollingDeadlineWriter {
	return newRollingDeadlineWriter(w, StallWindow(), bumpStep)
}

func newRollingDeadlineWriter(w http.ResponseWriter, window, step time.Duration) *RollingDeadlineWriter {
	s := &RollingDeadlineWriter{
		w:      w,
		rc:     http.NewResponseController(w),
		window: window,
		step:   step,
	}
	s.bump()
	return s
}

func (s *RollingDeadlineWriter) bump() {
	if s.disabled {
		return
	}
	if !s.lastBump.IsZero() && time.Since(s.lastBump) < s.step {
		return
	}
	s.forceBump()
}

// forceBump sets the deadline unconditionally, ignoring the step throttle.
//
// The throttle exists so a fast stream does not issue one SetWriteDeadline per
// 32 KB Write. A ReadFrom slice is already bounded at readFromChunk, so the
// throttle buys nothing there and costs correctness: a throttled slice starts
// with as little as window-step remaining, which raises the sustained rate a
// client must hold to survive from the documented 186 kbit/s to ~203 kbit/s and
// reaps healthy slow clients. Every slice therefore gets a full window, which is
// what the pre-CopyChunked loop did.
func (s *RollingDeadlineWriter) forceBump() {
	if s.disabled {
		return
	}
	now := time.Now()
	if err := s.rc.SetWriteDeadline(now.Add(s.window)); err != nil {
		s.disabled = true
		return
	}
	s.lastBump = now
}

func (s *RollingDeadlineWriter) Header() http.Header { return s.w.Header() }

func (s *RollingDeadlineWriter) WriteHeader(code int) {
	s.bump()
	if s.statusCode == 0 {
		s.statusCode = code
	}
	s.w.WriteHeader(code)
}

func (s *RollingDeadlineWriter) Write(p []byte) (int, error) {
	s.bump()
	if s.statusCode == 0 {
		s.statusCode = http.StatusOK
	}
	n, err := s.w.Write(p)
	s.recordWrite(int64(n), err)
	return n, err
}

// ReadFrom preserves the underlying ResponseWriter's io.ReaderFrom fast path
// (sendfile for *os.File bodies, as used by http.ServeContent) while still
// rolling the deadline between bounded slices.
func (s *RollingDeadlineWriter) ReadFrom(r io.Reader) (int64, error) {
	if s.statusCode == 0 {
		s.statusCode = http.StatusOK
	}
	// Before the FIRST slice as well as between slices: a handler that sets
	// headers and then waits on readiness before its first write would otherwise
	// run that slice against the window set at construction.
	s.forceBump()
	return ForwardReadFrom(s.w, s, r, readFromChunk, func(n int64, err error) {
		s.forceBump()
		s.recordWrite(n, err)
	})
}

func (s *RollingDeadlineWriter) Flush() {
	s.bump()
	_ = s.rc.Flush()
}

// StatusCode returns the response status observed by the wrapper.
func (s *RollingDeadlineWriter) StatusCode() int {
	return s.statusCode
}

// BytesWritten returns the number of response body bytes accepted by the
// underlying writer.
func (s *RollingDeadlineWriter) BytesWritten() int64 {
	return s.bytesWritten
}

// Outcome classifies the first write failure, or a canceled request when no
// write failure was surfaced by the transport.
func (s *RollingDeadlineWriter) Outcome(ctx context.Context) StreamOutcome {
	var ctxErr error
	if ctx != nil {
		ctxErr = ctx.Err()
	}
	return ClassifyOutcome(s.firstWriteErr, ctxErr)
}

// ClassifyOutcome classifies a streaming response from its first write error
// and request-context error. It is shared by every streaming writer so stalled
// connections have one definition throughout the server.
func ClassifyOutcome(firstWriteErr, ctxErr error) StreamOutcome {
	if isTimeoutError(firstWriteErr) {
		return OutcomeStalledReap
	}
	if firstWriteErr != nil || ctxErr != nil {
		return OutcomeClientGone
	}
	return OutcomeCompleted
}

// Unwrap lets http.ResponseController traverse to the underlying writer.
func (s *RollingDeadlineWriter) Unwrap() http.ResponseWriter { return s.w }

func (s *RollingDeadlineWriter) recordWrite(n int64, err error) {
	s.bytesWritten += n
	if err != nil && s.firstWriteErr == nil {
		s.firstWriteErr = err
	}
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

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
	"io"
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
	// SetWriteDeadline per step rather than one per 32 KB chunk.
	bumpStep = 15 * time.Second

	// readFromChunk bounds each ReadFrom slice so the deadline keeps rolling
	// during zero-copy (sendfile) transfers of large files.
	readFromChunk int64 = 64 << 20
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
	now := time.Now()
	if !s.lastBump.IsZero() && now.Sub(s.lastBump) < s.step {
		return
	}
	if err := s.rc.SetWriteDeadline(now.Add(s.window)); err != nil {
		s.disabled = true
		return
	}
	s.lastBump = now
}

func (s *RollingDeadlineWriter) Header() http.Header { return s.w.Header() }

func (s *RollingDeadlineWriter) WriteHeader(code int) {
	s.bump()
	s.w.WriteHeader(code)
}

func (s *RollingDeadlineWriter) Write(p []byte) (int, error) {
	s.bump()
	return s.w.Write(p)
}

// ReadFrom preserves the underlying ResponseWriter's io.ReaderFrom fast path
// (sendfile for *os.File bodies, as used by http.ServeContent) while still
// rolling the deadline between bounded slices.
func (s *RollingDeadlineWriter) ReadFrom(r io.Reader) (int64, error) {
	rf, ok := s.w.(io.ReaderFrom)
	if !ok {
		// writerOnly hides this method so io.Copy doesn't recurse into it.
		s.bump()
		return io.Copy(writerOnly{s}, r)
	}
	var total int64
	for {
		s.bump()
		n, err := rf.ReadFrom(io.LimitReader(r, readFromChunk))
		total += n
		if err != nil {
			return total, err
		}
		if n < readFromChunk {
			return total, nil
		}
	}
}

func (s *RollingDeadlineWriter) Flush() {
	s.bump()
	_ = s.rc.Flush()
}

// Unwrap lets http.ResponseController traverse to the underlying writer.
func (s *RollingDeadlineWriter) Unwrap() http.ResponseWriter { return s.w }

type writerOnly struct{ io.Writer }

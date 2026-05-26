package abs

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// accessLog is a minimal chi middleware that emits one structured line
// per request. The 2xx/3xx path logs at Debug so a default-Info runtime
// stays quiet during normal playback; non-2xx escalates to Warn so
// failures still surface without an explicit log-level flip. Path is
// captured query-less so ?token= and refresh tokens never land in logs.
func (h *Handler) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)

		auth := r.Header.Get("Authorization")
		authKind := "none"
		switch {
		case strings.HasPrefix(auth, "Bearer "):
			authKind = "bearer"
		case auth != "":
			authKind = "other"
		case r.URL.Query().Get("token") != "":
			authKind = "qtok"
		}

		// Short-circuit asset requests the mobile app never hits to keep
		// the signal-to-noise high.
		path := r.URL.Path
		if strings.HasPrefix(path, "/assets/") {
			return
		}

		args := []any{
			"method", r.Method,
			"path", path,
			"auth", authKind,
			"status", sw.status,
			"dur_ms", time.Since(start).Milliseconds(),
		}
		if sw.status >= 400 {
			slog.Warn("abs req failed", args...)
			return
		}
		slog.Debug("abs req", append(args, "bytes", sw.bytes)...)
	})
}

// statusRecorder lets the access log read the status code + bytes
// written without re-implementing http.ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Hijack passes through to the wrapped ResponseWriter so socket.io
// WebSocket upgrades can take ownership of the raw connection.
// Without this, the underlying engine.io transport sees a
// ResponseWriter that doesn't satisfy http.Hijacker and rejects the
// upgrade with `{"code":3,"message":"Bad request"}`.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

// Flush passes through to the wrapped ResponseWriter so chunked /
// server-sent-events responses (engine.io polling long-poll) flush
// promptly.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

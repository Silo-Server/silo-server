package activitylog

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

// excludedPrefixes are paths that should not be logged.
var excludedPrefixes = []string{
	"/api/v1/health",
	"/api/v1/ready",
	"/api/v1/admin/logs",
}

// streamPrefixes are paths logged only at session-start (not per-chunk).
var streamPrefixes = []string{
	"/api/v1/stream/",
	"/api/v1/playback/transcode/",
}

// LogContext is a mutable holder stored in context BEFORE auth middleware.
// Auth middleware populates it, and the activity log middleware reads it
// after the handler chain completes.
type LogContext struct {
	UserID             *int
	ImpersonatorUserID *int
	SessionID          string
}

type logContextKey struct{}

// PlaybackLogContext is a mutable holder for playback-specific correlation.
// Handlers update it once they know the playback session ID, and both the
// request logger and activity logger read it after the handler returns.
type PlaybackLogContext struct {
	PlaybackSessionID string
}

type playbackLogContextKey struct{}

// SetLogContext stores a LogContext pointer in the request context.
func SetLogContext(ctx context.Context, lc *LogContext) context.Context {
	return context.WithValue(ctx, logContextKey{}, lc)
}

// GetLogContext retrieves the LogContext from the request context.
func GetLogContext(ctx context.Context) *LogContext {
	lc, _ := ctx.Value(logContextKey{}).(*LogContext)
	return lc
}

func SetPlaybackLogContext(ctx context.Context, lc *PlaybackLogContext) context.Context {
	return context.WithValue(ctx, playbackLogContextKey{}, lc)
}

func GetPlaybackLogContext(ctx context.Context) *PlaybackLogContext {
	lc, _ := ctx.Value(playbackLogContextKey{}).(*PlaybackLogContext)
	return lc
}

// NewMiddleware returns chi-compatible middleware that logs every request
// to the given Writer. It wraps ResponseWriter to capture the status code.
// It stores a mutable LogContext in the request context that downstream auth
// middleware can populate with user info.
func NewMiddleware(w Writer, nodeID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Skip excluded endpoints
			for _, prefix := range excludedPrefixes {
				if strings.HasPrefix(path, prefix) {
					next.ServeHTTP(rw, r)
					return
				}
			}

			// Skip per-chunk stream requests (segments, manifests after start)
			for _, prefix := range streamPrefixes {
				if strings.HasPrefix(path, prefix) && isStreamChunk(path) {
					next.ServeHTTP(rw, r)
					return
				}
			}

			start := time.Now()
			wrapped := &statusWriter{ResponseWriter: rw, status: http.StatusOK}

			// Reuse any existing LogContext so request logging and activity logging
			// observe the same auth-populated values.
			lc := GetLogContext(r.Context())
			if lc == nil {
				lc = &LogContext{}
				ctx := SetLogContext(r.Context(), lc)
				r = r.WithContext(ctx)
			}
			playbackLC := GetPlaybackLogContext(r.Context())
			if playbackLC == nil {
				playbackLC = &PlaybackLogContext{}
				ctx := SetPlaybackLogContext(r.Context(), playbackLC)
				r = r.WithContext(ctx)
			}

			next.ServeHTTP(wrapped, r)

			pathPattern := path
			if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
				if route := routeCtx.RoutePattern(); route != "" {
					pathPattern = route
				}
			}
			path = RedactSecretPathParams(r, path)

			// After handler chain completes, auth middleware has populated lc
			entry := LogEntry{
				Timestamp:          start,
				ClientIP:           clientip.FromContext(r.Context()),
				UserID:             lc.UserID,
				ImpersonatorUserID: lc.ImpersonatorUserID,
				SessionID:          lc.SessionID,
				PlaybackSessionID:  playbackLC.PlaybackSessionID,
				RequestID:          middleware.GetReqID(r.Context()),
				NodeID:             nodeID,
				Method:             r.Method,
				Path:               path,
				PathPattern:        pathPattern,
				StatusCode:         wrapped.status,
				UserAgent:          r.UserAgent(),
				DurationMs:         int(time.Since(start).Milliseconds()),
			}

			w.Write(entry)
		})
	}
}

// RedactSecretPathParams strips bearer credentials from a request path before
// it reaches any log sink. Public webhook endpoints authenticate via a secret
// path segment (chi route params named "token" or "secret" — autoscan webhook
// intake, webhook-sync); logging the raw path would persist the credential in
// app logs and the activity table. Must be called after the handler chain ran
// so the chi route context is populated.
func RedactSecretPathParams(r *http.Request, path string) string {
	routeCtx := chi.RouteContext(r.Context())
	if routeCtx == nil {
		return path
	}
	for i, key := range routeCtx.URLParams.Keys {
		if key != "token" && key != "secret" {
			continue
		}
		if i >= len(routeCtx.URLParams.Values) {
			continue
		}
		value := routeCtx.URLParams.Values[i]
		if value == "" {
			continue
		}
		path = strings.Replace(path, "/"+value, "/[redacted]", 1)
	}
	return path
}

// isStreamChunk returns true if the path looks like a stream segment/manifest
// chunk rather than a session-start request.
func isStreamChunk(path string) bool {
	return strings.Contains(path, "/segment/") ||
		strings.Contains(path, "/master.m3u8") ||
		strings.Contains(path, "/subtitles/")
}

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// Hijack implements http.Hijacker, required for WebSocket upgrades.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Unwrap returns the underlying ResponseWriter, preserving http.Flusher,
// http.Hijacker, etc. (Go 1.20+ ResponseController pattern).
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Flush implements http.Flusher directly as well: callers (and middlewares
// like chi's Compress) discover flushing via a plain type assertion, which
// Unwrap alone does not satisfy.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

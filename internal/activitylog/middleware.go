package activitylog

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/logstream"
)

// excludedPrefixes are paths that should not be logged: the retained
// operational probes, and the admin log endpoints, which the log viewer
// polls (logging a read of the activity log into the activity log feeds
// itself). /api/v2/admin/logs covers the app, audit, ws, ws-ticket and
// ws/capabilities operations under that prefix. Transcode nodes call
// /api/internal/nodes/ every second to report and ship their logs: those calls
// are machine traffic, not activity, and a logged log shipment would feed
// itself too.
var excludedPrefixes = []string{
	"/api/v1/health",
	"/api/v1/ready",
	"/api/v2/admin/logs",
	"/api/internal/nodes/",
}

// streamPrefixes are paths logged once per play, not per request. A segment or
// manifest chunk (isStreamChunk) writes no row. A range request that continues a
// play (isRangeContinuation) writes none either when it succeeds or its client
// abandons it; one that fails keeps its row, so a refused or broken range stays
// visible, and so does a GET answered 200, which is a stream started again from
// its origin. A request with no Range header, or a range from byte 0, is a
// play's start and writes a row. An answered CORS preflight is no play and
// writes none.
var streamPrefixes = []string{
	"/api/v2/stream/",
	"/api/v2/playback/transcode/",
}

// LogContext is a mutable holder stored in context BEFORE auth middleware.
// Auth middleware populates it, and the activity log middleware reads it
// after the handler chain completes.
type LogContext = auditmutation.LogContext

var SetLogContext = auditmutation.SetLogContext
var GetLogContext = auditmutation.GetLogContext

// PlaybackLogContext is a mutable holder for playback-specific correlation.
// Handlers update it once they know the playback session ID, and both the
// request logger and activity logger read it after the handler returns.
type PlaybackLogContext struct {
	PlaybackSessionID string
}

type playbackLogContextKey struct{}

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
func NewMiddleware(w Writer, nodeID string, hubs ...*logstream.Hub) func(http.Handler) http.Handler {
	return NewFilteredMiddleware(w, nodeID, nil, hubs...)
}

// NewFilteredMiddleware is NewMiddleware for a router that also serves requests
// the activity log should not record. skipRoute receives the matched chi route
// pattern after the handler returns and drops the entry when it reports true;
// matching the pattern rather than the raw path keeps the filter independent
// of any path rewriting the router does. A nil skipRoute records every request
// NewMiddleware would.
func NewFilteredMiddleware(w Writer, nodeID string, skipRoute func(pattern string) bool, hubs ...*logstream.Hub) func(http.Handler) http.Handler {
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
			onStreamPath := slices.ContainsFunc(streamPrefixes, func(prefix string) bool { return strings.HasPrefix(path, prefix) })
			if onStreamPath && isStreamChunk(path) {
				next.ServeHTTP(rw, r)
				return
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

			lc.Request, lc.NodeID, lc.Started = r, nodeID, start
			if len(hubs) > 0 {
				lc.Publish = func(entry AuditEntry) {
					_, _ = logstream.PublishAppends(hubs[0], logstream.StreamAudit, []AuditEntry{entry})
				}
			}
			next.ServeHTTP(wrapped, r)

			pathPattern := path
			if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
				if route := routeCtx.RoutePattern(); route != "" {
					pathPattern = route
				}
			}
			if skipRoute != nil && skipRoute(pathPattern) {
				return
			}
			status := RecordedStatus(r, wrapped.status, wrapped.wroteHeader)
			if onStreamPath && skipsStreamRow(r, status) {
				return
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
				StatusCode:         status,
				UserAgent:          auditmutation.BoundUserAgent(r.UserAgent()),
				DurationMs:         int(time.Since(start).Milliseconds()),
			}

			// Domain rows already persisted atomically; avoid a second success
			// row for the same operation, but still count it as the account's
			// activity. Preserve a later HTTP failure separately.
			for _, committed := range lc.Committed {
				if committed.StatusCode == wrapped.status {
					entry.Persisted = true
					break
				}
			}
			w.Write(entry)
		})
	}
}

// StatusClientClosedRequest is the status recorded for a request its client
// abandoned, as nginx records it.
const StatusClientClosedRequest = 499

// RecordedStatus is the status to record for a request that ended with
// status, where wrote says whether a response was started. A request whose
// client went away and that got no response, or a server error, is recorded
// as StatusClientClosedRequest: the client never saw an answer, and the
// failure is not one the server has to count.
func RecordedStatus(r *http.Request, status int, wrote bool) int {
	if errors.Is(r.Context().Err(), context.Canceled) && (!wrote || status >= 500) {
		return StatusClientClosedRequest
	}
	return status
}

// RedactSecretPathParams strips bearer credentials from a request path before
// it reaches any log sink (auditmutation.RedactSecretPath). Must be called
// after the handler chain ran so the chi route context is populated.
func RedactSecretPathParams(r *http.Request, path string) string {
	return auditmutation.RedactSecretPath(chi.RouteContext(r.Context()), path)
}

// isStreamChunk returns true if the path looks like a stream segment/manifest
// chunk rather than a session-start request. The match is on path shape, not
// on the API major: /api/v{1,2}/stream/{session_id} is a session start, while
// its /subtitles/{track}[/fonts] children and the
// /playback/transcode/{session_id}/{master.m3u8,segment/{name}} routes are
// per-chunk fetches.
func isStreamChunk(path string) bool {
	return strings.Contains(path, "/segment/") ||
		strings.Contains(path, "/master.m3u8") ||
		strings.Contains(path, "/subtitles/")
}

// skipsStreamRow reports whether a finished request on a stream path writes no
// row. A range that continues a play adds a row only by failing; an abandoned
// one is a seek or a stripe deadline, which the client does on purpose. A GET
// answered 200 sent the stream again from its start, as a progressive remux
// does for a range past byte 0, so it keeps its row. An answered OPTIONS is the
// media origin's CORS preflight.
func skipsStreamRow(r *http.Request, status int) bool {
	if status >= http.StatusBadRequest && status != StatusClientClosedRequest {
		return false
	}
	if r.Method == http.MethodOptions {
		return true
	}
	if r.Method == http.MethodGet && status == http.StatusOK {
		return false
	}
	return isRangeContinuation(r.Header.Get("Range"))
}

// isRangeContinuation reports whether a Range header asks for bytes after a
// play's start: a bytes range whose first range starts past byte 0, or a suffix
// range. A value that does not parse is no continuation, so its request stays
// visible.
func isRangeContinuation(header string) bool {
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(spec, ",")
	from, to, ok := strings.Cut(first, "-")
	if !ok {
		return false
	}
	if from == "" {
		// bytes=-N: the last N bytes.
		n, err := strconv.ParseUint(to, 10, 64)
		return err == nil && n > 0
	}
	start, err := strconv.ParseUint(from, 10, 64)
	if err != nil || start == 0 {
		return false
	}
	if to == "" {
		return true
	}
	_, err = strconv.ParseUint(to, 10, 64)
	return err == nil
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

func (w *statusWriter) ReadFrom(src io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = http.StatusOK, true
	}
	return httpstream.ForwardReadFrom(w.ResponseWriter, w, src, 0, nil)
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

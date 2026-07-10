package middleware

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

func RequestLogger(nodeID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, prefix := range []string{"/api/v1/health", "/api/v1/ready", "/api/v1/admin/logs"} {
				if strings.HasPrefix(r.URL.Path, prefix) {
					next.ServeHTTP(w, r)
					return
				}
			}

			start := time.Now()
			wrapped := &requestStatusWriter{ResponseWriter: w, status: http.StatusOK}
			lc := activitylog.GetLogContext(r.Context())
			if lc == nil {
				lc = &activitylog.LogContext{}
				r = r.WithContext(activitylog.SetLogContext(r.Context(), lc))
			}
			playbackLC := activitylog.GetPlaybackLogContext(r.Context())
			if playbackLC == nil {
				playbackLC = &activitylog.PlaybackLogContext{}
				r = r.WithContext(activitylog.SetPlaybackLogContext(r.Context(), playbackLC))
			}

			next.ServeHTTP(wrapped, r)

			pathPattern := r.URL.Path
			if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
				if route := routeCtx.RoutePattern(); route != "" {
					pathPattern = route
				}
			}

			attrs := []any{
				"component", "api",
				"request_id", chimw.GetReqID(r.Context()),
				"method", r.Method,
				"path", activitylog.RedactSecretPathParams(r, r.URL.Path),
				"path_pattern", pathPattern,
				"status", wrapped.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"client_ip", clientip.FromContext(r.Context()),
				"user_agent", r.UserAgent(),
				"node_id", nodeID,
			}
			if lc.UserID != nil {
				attrs = append(attrs, "user_id", *lc.UserID)
			}
			if lc.SessionID != "" {
				attrs = append(attrs, "session_id", lc.SessionID)
			}
			if playbackLC.PlaybackSessionID != "" {
				attrs = append(attrs, "playback_session_id", playbackLC.PlaybackSessionID)
			}
			slog.InfoContext(r.Context(), "api request", append([]any{"component", "api"}, attrs...)...)
		})
	}
}

type requestStatusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *requestStatusWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestStatusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *requestStatusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Flush implements http.Flusher so progressive responses (streamed subtitle
// extracts, remux output) keep flushing through the logging wrapper instead of
// silently buffering until the handler returns.
func (w *requestStatusWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying connection (e.g.
// for the per-response write deadlines used by streaming handlers).
func (w *requestStatusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

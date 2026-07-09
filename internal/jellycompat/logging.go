package jellycompat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *loggingResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func requestLoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &loggingResponseWriter{ResponseWriter: w}

		next.ServeHTTP(ww, r)

		routePattern := ""
		if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
			routePattern = routeCtx.RoutePattern()
		}

		slog.InfoContext(r.Context(), "jellycompat request", "component", "jellycompat",
			"request_id", middleware.GetReqID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"original_path", firstNonEmpty(originalPathFromContext(r.Context()), r.URL.Path),
			"query", r.URL.RawQuery,
			"route", routePattern,
			"status", statusOrDefault(ww.status),
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
			"content_length", r.ContentLength,
			"auth_kind", authKind(r),
		)
	})
}

func statusOrDefault(status int) int {
	if status == 0 {
		return http.StatusOK
	}
	return status
}

// debugMaxBodyCapture is the maximum response body size captured for debug logging.
const debugMaxBodyCapture = 256 * 1024 // 256 KB

// debugResponseWriter captures the response status and body for debug logging.
type debugResponseWriter struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	captured    int
	totalBytes  int
	detected    bool
	skipBody    bool
	contentType string
}

func (w *debugResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *debugResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if !w.detected {
		ct := strings.TrimSpace(w.Header().Get("Content-Type"))
		if ct == "" && len(b) > 0 {
			sniffLen := len(b)
			if sniffLen > 512 {
				sniffLen = 512
			}
			ct = http.DetectContentType(b[:sniffLen])
		}
		w.contentType = ct
		w.skipBody = !isTextualContentType(ct)
		w.detected = true
	}
	w.totalBytes += len(b)
	if !w.skipBody && w.captured < debugMaxBodyCapture {
		remaining := debugMaxBodyCapture - w.captured
		toCapture := len(b)
		if toCapture > remaining {
			toCapture = remaining
		}
		w.body.Write(b[:toCapture])
		w.captured += toCapture
	}
	return w.ResponseWriter.Write(b)
}

func (w *debugResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// newDebugLogMiddleware creates a middleware that logs full request/response
// pairs to the given file. Enable by setting JELLYCOMPAT_DEBUG_LOG=/path/to/file.
// When userAgentFilter is non-empty, only requests whose User-Agent contains
// the filter string (case-insensitive) are logged.
func newDebugLogMiddleware(logFile io.Writer, userAgentFilter string) func(http.Handler) http.Handler {
	var mu sync.Mutex
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if userAgentFilter != "" && !strings.Contains(strings.ToLower(r.UserAgent()), strings.ToLower(userAgentFilter)) {
				next.ServeHTTP(w, r)
				return
			}

			// Capture request body for POST/PUT/PATCH.
			var reqBody []byte
			if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
				reqBody, _ = io.ReadAll(io.LimitReader(r.Body, debugMaxBodyCapture))
				r.Body = io.NopCloser(bytes.NewReader(reqBody))
			}

			start := time.Now()
			dw := &debugResponseWriter{ResponseWriter: w}
			next.ServeHTTP(dw, r)
			elapsed := time.Since(start)

			reqID := middleware.GetReqID(r.Context())
			status := statusOrDefault(dw.status)

			mu.Lock()
			defer mu.Unlock()

			fmt.Fprintf(logFile, "=== %s %s %s [%s] %dms ===\n",
				start.Format("2006/01/02 15:04:05"),
				r.Method,
				r.URL.String(),
				reqID,
				elapsed.Milliseconds(),
			)
			fmt.Fprintf(logFile, "Remote: %s  User-Agent: %s\n", r.RemoteAddr, r.UserAgent())
			fmt.Fprintf(logFile, "Status: %d\n", status)

			if len(reqBody) > 0 {
				fmt.Fprintf(logFile, "Request Body (%d bytes):\n", len(reqBody))
				writeIndentedJSON(logFile, reqBody)
			}

			switch {
			case dw.body.Len() > 0:
				fmt.Fprintf(logFile, "Response (content-type=%s, %d bytes):\n", displayContentType(dw.contentType), dw.totalBytes)
				if dw.captured >= debugMaxBodyCapture {
					fmt.Fprintf(logFile, "[truncated at %d bytes]\n", debugMaxBodyCapture)
				}
				writeIndentedJSON(logFile, dw.body.Bytes())
			case dw.skipBody && dw.totalBytes > 0:
				fmt.Fprintf(logFile, "Response: [binary content-type=%s bytes=%d]\n", displayContentType(dw.contentType), dw.totalBytes)
			default:
				fmt.Fprintln(logFile, "Response: (empty)")
			}
			fmt.Fprintln(logFile)
		})
	}
}

// writeIndentedJSON pretty-prints b if it's valid JSON, otherwise writes it as
// UTF-8 text. If b is not valid UTF-8 (e.g. a handler lied about Content-Type),
// it is omitted so the log file stays readable.
func writeIndentedJSON(w io.Writer, b []byte) {
	var buf bytes.Buffer
	if json.Indent(&buf, b, "", "  ") == nil {
		buf.WriteByte('\n')
		w.Write(buf.Bytes())
		return
	}
	if utf8.Valid(b) {
		w.Write(b)
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintf(w, "[non-UTF-8 payload, %d bytes omitted]\n", len(b))
}

// isTextualContentType reports whether a captured body with Content-Type ct is
// safe to write verbatim into the text log file.
func isTextualContentType(ct string) bool {
	if ct == "" {
		return false
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return false
	}
	switch ct {
	case "application/json",
		"application/xml",
		"application/x-mpegurl",
		"application/vnd.apple.mpegurl",
		"application/javascript",
		"application/ecmascript":
		return true
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	if strings.HasPrefix(ct, "application/") &&
		(strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml")) {
		return true
	}
	return false
}

func displayContentType(ct string) string {
	if ct == "" {
		return "unknown"
	}
	return ct
}

func authKind(r *http.Request) string {
	for _, headerName := range []string{"Authorization", "X-Emby-Authorization"} {
		header := strings.TrimSpace(r.Header.Get(headerName))
		lower := strings.ToLower(header)
		switch {
		case strings.HasPrefix(lower, "mediabrowser "):
			return "mediabrowser"
		case strings.HasPrefix(lower, "emby "):
			return "mediabrowser"
		case strings.HasPrefix(lower, "bearer "):
			return "bearer"
		}
	}
	switch {
	case strings.TrimSpace(r.Header.Get("X-Emby-Token")) != "":
		return "x-emby-token"
	case strings.TrimSpace(r.Header.Get("X-Mediabrowser-Token")) != "":
		return "x-mediabrowser-token"
	case strings.TrimSpace(newCaseInsensitiveQuery(r.URL.Query()).Get("ApiKey")) != "":
		return "api_key"
	case strings.TrimSpace(newCaseInsensitiveQuery(r.URL.Query()).Get("api_key")) != "":
		return "api_key"
	default:
		return "none"
	}
}

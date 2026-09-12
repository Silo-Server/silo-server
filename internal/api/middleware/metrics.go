package middleware

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "streamapp_http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "streamapp_http_request_duration_seconds",
			Help:    "Duration of HTTP requests in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
)

// Metrics is an HTTP middleware that records request count and duration.
func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, APIv2Prefix) {
			// The v2 listener records itself with operation-ID labels.
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()

		// Wrap response writer to capture status code.
		wrapped := &statusWriter{ResponseWriter: w, status: 200}

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start).Seconds()
		path := "unmatched"
		if route := chi.RouteContext(r.Context()); route != nil && route.RoutePattern() != "" {
			path = route.RoutePattern()
		}
		method := metricMethod(r.Method)

		httpRequestsTotal.WithLabelValues(method, path, strconv.Itoa(wrapped.status)).Inc()
		httpRequestDuration.WithLabelValues(method, path).Observe(duration)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.written {
		w.status = status
		w.written = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.status, w.written = http.StatusOK, true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) ReadFrom(src io.Reader) (int64, error) {
	if !w.written {
		w.status, w.written = http.StatusOK, true
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

// Flush implements http.Flusher so progressive responses keep flushing
// through the metrics wrapper instead of silently buffering.
func (w *statusWriter) Flush() {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying connection (e.g.
// for the per-response write deadlines used by streaming handlers).
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// metricMethod prevents arbitrary HTTP tokens from allocating metric series.
func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	default:
		return "other"
	}
}

package streamtelemetry

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

const OutcomeUnknown httpstream.StreamOutcome = "unknown"

func (r *Registry) Observe(route MediaRoute) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Evaluated once at mount time — Observe returns the middleware, and the
		// closure below is what runs per request — so the family gate costs
		// nothing on the hot path.
		if r == nil || !r.cfg.Enabled || !route.Enrolled || !r.cfg.ObservesFamily(route.Family) {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			var capture CaptureSet
			if route.Capture != nil {
				capture = route.Capture(request)
				if capture.Method == "" {
					capture.Method = request.Method
				}
				if capture.Pattern == "" {
					capture.Pattern = route.Pattern
				}
				if capture.ReceivedAt.IsZero() {
					capture.ReceivedAt = now()
				}
			} else {
				capture = genericCapture(request)
			}
			obs := r.begin(route, capture)
			observed := &observedWriter{w: w, observation: obs, bodyEligible: request.Method != http.MethodHead}
			request = request.WithContext(context.WithValue(request.Context(), observationContextKey{}, obs))
			completed := false
			defer func() {
				r.release(obs, obs.outcome(request.Context().Err(), completed))
			}()
			next.ServeHTTP(observed, request)
			completed = true
		})
	}
}

type observedWriter struct {
	w            http.ResponseWriter
	observation  *Observation
	bodyEligible bool
	statusCode   int
}

func (w *observedWriter) Header() http.Header { return w.w.Header() }

func (w *observedWriter) WriteHeader(statusCode int) {
	if w.statusCode == 0 {
		w.statusCode = statusCode
	}
	w.w.WriteHeader(statusCode)
}

func (w *observedWriter) Write(p []byte) (int, error) {
	if w.observation.cut.Load() {
		return 0, context.Canceled
	}
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.w.Write(p)
	if w.bodyEligible {
		w.observation.AddBytes(int64(n))
	}
	w.observation.recordWriteError(err)
	return n, err
}

// ReadFrom samples the cut flag once, at entry, whereas Write samples it every
// ~32 KB. That difference is protocol-visible: on HTTP/1.1 the h1 writer is an
// io.ReaderFrom, so a cut cannot interrupt an in-flight transfer and a 20 GB
// direct play drains to the end; on HTTP/2 there is no ReaderFrom, the fallback
// io.Copy goes through Write, and the same cut lands within 32 KB.
//
// Latent today — nothing calls cut.Store and this package is observational
// (doc.go) — and deliberately left that way rather than half-fixed: a per-slice
// cut check here would still only act at readFromChunk granularity, so the two
// protocols would still disagree, just less visibly. The enforcement change that
// introduces a caller for cut.Store owns making the granularity uniform, and
// must land with a test that a cut behaves identically over h1 and h2.
func (w *observedWriter) ReadFrom(reader io.Reader) (int64, error) {
	if w.observation.cut.Load() {
		return 0, context.Canceled
	}
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return httpstream.ForwardReadFrom(w.w, w, reader, httpstream.ReadFromChunkDefault, func(n int64, err error) {
		if w.bodyEligible {
			w.observation.AddBytes(n)
		}
		w.observation.recordWriteError(err)
	})
}

func (w *observedWriter) Unwrap() http.ResponseWriter { return w.w }
func (w *observedWriter) Flush()                      { _ = http.NewResponseController(w.w).Flush() }

func (w *observedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.w.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *observedWriter) Push(target string, options *http.PushOptions) error {
	pusher, ok := w.w.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

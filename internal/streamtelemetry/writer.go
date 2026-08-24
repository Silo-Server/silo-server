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

// ReadFrom is the one wrapper that does not use httpstream.ForwardReadFrom: it
// needs a per-slice continuation check for the cut flag, which the shared helper
// deliberately does not carry.
func (w *observedWriter) ReadFrom(reader io.Reader) (int64, error) {
	if w.observation.cut.Load() {
		return 0, context.Canceled
	}
	readerFrom, ok := httpstream.ReaderFromOf(w.w)
	if !ok {
		// WriterOnly hides ReadFrom so io.Copy cannot recurse into this method;
		// the bytes then route through Write, which samples the cut itself.
		return io.Copy(httpstream.WriterOnly(w), reader)
	}
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	// Re-check the cut BETWEEN slices, not once at entry. Write samples it every
	// ~32 KiB, and the HTTP/2 writer has no ReaderFrom so it falls back to that
	// path; without a per-slice check an HTTP/1.1 sendfile would keep pouring
	// until the whole file drained, making a kill switch behave differently by
	// protocol. A slice is as fine as this path gets: once sendfile is in flight
	// the kernel does not call back into Go.
	return httpstream.CopyChunkedUntil(readerFrom, reader, httpstream.ReadFromChunkDefault, func(n int64, err error) {
		if w.bodyEligible {
			w.observation.AddBytes(n)
		}
		w.observation.recordWriteError(err)
	}, func() error {
		if w.observation.cut.Load() {
			return context.Canceled
		}
		return nil
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

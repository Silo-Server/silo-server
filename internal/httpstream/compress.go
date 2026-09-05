package httpstream

import (
	"bufio"
	"errors"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// CompressExcept mounts chi's compressor but leaves the ResponseWriter
// untouched when skip reports true, preserving io.ReaderFrom on bulk routes.
func CompressExcept(level int, skip func(*http.Request) bool, types ...string) func(http.Handler) http.Handler {
	return CompressWithExclusions(level, skip, nil, types...)
}

// CompressWithExclusions is CompressExcept with a second, response-time
// exclusion. skipRequest is decided before the handler runs, as in
// CompressExcept. skipResponse is decided when the handler writes its status,
// once the response header is known: a response it reports bypasses the
// encoder and reaches the wire identity-encoded with no Content-Encoding or
// Vary added, while the rest of the chain still sees the bytes. Either
// exclusion may be nil.
func CompressWithExclusions(level int, skipRequest func(*http.Request) bool, skipResponse func(*http.Request, http.Header) bool, types ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		inner := next
		if skipResponse != nil {
			inner = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(&responseExclusionWriter{cw: w, w: w, r: r, skip: skipResponse}, r)
			})
		}
		compressed := middleware.Compress(level, types...)(inner)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skipRequest != nil && skipRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			compressed.ServeHTTP(w, r)
		})
	}
}

// responseExclusionWriter sits between the handler and the compressor's
// writer. On the first status write it asks skip whether this response must
// stay identity-encoded; if so it writes to the writer directly beneath the
// compressor (chi's compressResponseWriter.Unwrap), so only the encoder is
// bypassed and the encoder never sees a byte. The header map is shared with
// the compressor's writer, so a handler that sets a header before or after
// the decision sees no difference.
type responseExclusionWriter struct {
	cw      http.ResponseWriter
	w       http.ResponseWriter
	r       *http.Request
	skip    func(*http.Request, http.Header) bool
	decided bool
}

func (x *responseExclusionWriter) Header() http.Header { return x.cw.Header() }

func (x *responseExclusionWriter) WriteHeader(code int) {
	if !x.decided {
		x.decided = true
		if x.skip(x.r, x.cw.Header()) {
			if u, ok := x.cw.(interface{ Unwrap() http.ResponseWriter }); ok {
				x.w = u.Unwrap()
			}
		}
	}
	x.w.WriteHeader(code)
}

func (x *responseExclusionWriter) Write(p []byte) (int, error) {
	if !x.decided {
		x.WriteHeader(http.StatusOK)
	}
	return x.w.Write(p)
}

// The optional ResponseWriter interfaces are delegated to whichever writer
// is current (the compressor's, or the one beneath it once excluded) so the
// wrapper is transparent to handlers that need them. Hijack is what the
// WebSocket upgrader asserts before it has written anything: gorilla answers
// 500 when the writer it is handed lacks it. io.ReaderFrom is deliberately
// absent: the compressor's writer does not offer it either, and the bulk
// routes that rely on sendfile bypass the wrapper through skipRequest.

func (x *responseExclusionWriter) Flush() {
	_ = http.NewResponseController(x.w).Flush()
}

func (x *responseExclusionWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(x.w).Hijack()
}

func (x *responseExclusionWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := x.w.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return errors.New("httpstream: http.Pusher is unavailable on the writer")
}

// Unwrap exposes the compressor's writer so http.ResponseController reaches
// the connection through the same chain as an unexcluded response.
func (x *responseExclusionWriter) Unwrap() http.ResponseWriter { return x.cw }

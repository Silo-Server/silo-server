package httpstream

import (
	"bufio"
	"bytes"
	"io"
	"maps"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// defaultCompressibleTypes are the media types the compressor encodes when a
// listener names none. HLS playlists and SRT/ASS subtitles are deliberately
// absent: their handlers state Content-Length, and a ranged reader such as
// AetherEngine on Apple platforms opens a length-less 200 as a non-seekable
// stream.
var defaultCompressibleTypes = []string{
	"text/html",
	"text/css",
	"text/plain",
	"text/javascript",
	"text/markdown",
	"text/csv",
	"text/vtt",
	"application/javascript",
	"application/x-javascript",
	"application/json",
	"application/problem+json",
	"application/manifest+json",
	"application/wasm",
	"application/atom+xml",
	"application/rss+xml",
	"application/xml",
	"text/xml",
	"image/svg+xml",
}

// CompressExcept mounts the compressor but leaves the ResponseWriter
// untouched when skip reports true, preserving io.ReaderFrom on bulk routes.
func CompressExcept(skip func(*http.Request) bool, types ...string) func(http.Handler) http.Handler {
	return CompressWithExclusions(skip, nil, types...)
}

// CompressWithExclusions encodes responses of the given media types (the
// default list when none are named) in the content coding the request's
// Accept-Encoding weights highest (see negotiate), and adds
// Vary: Accept-Encoding to every response whose coding it negotiates,
// including those that stay identity, so a cache keeps one per coding.
//
// It has two exclusions. skipRequest is decided before the handler runs, and
// a request it reports gets the ResponseWriter untouched, as in
// CompressExcept. skipResponse is decided when the handler writes its status,
// once the response header is known: a response it reports reaches the wire
// identity-encoded with no Content-Encoding or Vary added. Either exclusion
// may be nil. Responses that already declare a content coding or a
// Content-Range, and 204, 206 and 304 responses, are never negotiated. An
// encoded response keeps a Content-Length, of the encoded body, when its
// handler declared one of at most maxHeldLength and did not flush.
func CompressWithExclusions(skipRequest func(*http.Request) bool, skipResponse func(*http.Request, http.Header) bool, types ...string) func(http.Handler) http.Handler {
	if len(types) == 0 {
		types = defaultCompressibleTypes
	}
	c := &compressor{types: make(map[string]struct{}, len(types)), skipResponse: skipResponse}
	for _, t := range types {
		c.types[t] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skipRequest != nil && skipRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			coding, identityRefused := negotiate(r.Header.Values("Accept-Encoding"))
			cw := &compressWriter{ResponseWriter: w, r: r, c: c, coding: coding, identityRefused: identityRefused}
			defer cw.close()
			next.ServeHTTP(cw, r)
		})
	}
}

type compressor struct {
	types        map[string]struct{}
	skipResponse func(*http.Request, http.Header) bool
}

// negotiates reports whether the coding of a response with this status and
// header depends on Accept-Encoding. It runs after skipResponse, which may
// have declared one.
func (c *compressor) negotiates(status int, h http.Header) bool {
	if status == http.StatusNoContent || status == http.StatusNotModified || status == http.StatusPartialContent {
		return false
	}
	// A Content-Range names identity bytes, and a coding already declared
	// (identity included) is the handler's decision.
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	mediaType, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	_, ok := c.types[strings.ToLower(strings.TrimSpace(mediaType))]
	return ok
}

// encodes reports whether a negotiated response gets the request's coding. A
// client that refused identity gets even a short body encoded.
func (w *compressWriter) encodes(h http.Header) bool {
	if w.coding == nil {
		return false
	}
	if w.identityRefused {
		return true
	}
	// A body declared shorter than minEncodedLength comes out no smaller once
	// the coding's framing and the Content-Encoding line are counted.
	n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	return err != nil || n >= minEncodedLength
}

// minEncodedLength is the smallest declared Content-Length the compressor
// encodes. Playback progress replies (about 80 bytes) grew by a fifth under
// gzip before the headers were counted.
const minEncodedLength = 256

// maxHeldLength is the largest declared Content-Length whose encoded body the
// compressor holds until the handler returns, so it can go out in one write
// with its encoded length. Without a length, net/http sends a body that
// outgrows its 2 KiB buffer chunked, and closes the connection after it for an
// HTTP/1.0 client. A longer body, or one whose handler flushes, streams.
const maxHeldLength = 8 << 20

// maxPooledHeld is the largest held-body buffer kept for reuse, so a
// rare large response does not pin its buffer for the small ones after it.
const maxPooledHeld = 64 << 10

var heldBodies sync.Pool

// heldBody is where a held response's encoder writes: into buf until the
// response streams, then to out.
type heldBody struct {
	buf bytes.Buffer
	out io.Writer
}

func (b *heldBody) Write(p []byte) (int, error) {
	if b.out != nil {
		return b.out.Write(p)
	}
	return b.buf.Write(p)
}

// negotiate returns the coding to apply for the Accept-Encoding field lines,
// or nil for identity: the offered coding with the highest weight, ties going
// to the earlier entry in codings, unless identity outweighs it. A coding
// weighted q=0 is refused, `*` sets the weight of every coding the field does
// not name (identity included), x-gzip names gzip, and an element with a
// malformed weight is ignored (RFC 9110 12.5.3). A missing or empty field gets
// identity. identityRefused reports a field that weights identity q=0.
func negotiate(values []string) (best *coding, identityRefused bool) {
	var weights [len(codings)]float64
	for i := range weights {
		weights[i] = -1
	}
	star, identity := -1.0, -1.0
	for _, value := range values {
		for element := range strings.SplitSeq(value, ",") {
			name, params, _ := strings.Cut(element, ";")
			name = strings.TrimSpace(name)
			q, ok := qValue(params)
			if !ok || name == "" {
				continue
			}
			if name == "*" {
				star = q
				continue
			}
			if strings.EqualFold(name, "identity") {
				identity = q
				continue
			}
			for i, c := range codings {
				if strings.EqualFold(name, c.name) || (c.alias != "" && strings.EqualFold(name, c.alias)) {
					weights[i] = q
				}
			}
		}
	}
	bestQ := 0.0
	for i, q := range weights {
		if q < 0 {
			q = star
		}
		if q > bestQ {
			best, bestQ = codings[i], q
		}
	}
	if identity < 0 {
		identity = star
	}
	if identity > bestQ {
		best = nil
	}
	return best, identity == 0
}

// qValue reads the weight from an element's parameters: 1 when it has none,
// and false when the weight is not a number in [0, 1].
func qValue(params string) (float64, bool) {
	q := 1.0
	for param := range strings.SplitSeq(params, ";") {
		key, value, _ := strings.Cut(strings.TrimSpace(param), "=")
		if !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || v < 0 || v > 1 {
			return 0, false
		}
		q = v
	}
	return q, true
}

// compressWriter decides on the first final status whether the response is
// encoded, and takes an encoder from the coding's pool only then, so a
// response that stays identity never touches one. An encoded response with a
// declared length of at most maxHeldLength is held: its status, header and
// encoded body reach the ResponseWriter only at close, or when it streams.
type compressWriter struct {
	http.ResponseWriter
	r       *http.Request
	c       *compressor
	coding  *coding
	enc     encoder
	decided bool
	// identityRefused lets a short body be encoded (see compressor.encodes).
	identityRefused bool
	// held is a held response's encoder destination, and status and header
	// are what it was written with; remaining counts down its declared length.
	held      *heldBody
	status    int
	header    http.Header
	remaining int64
}

func (w *compressWriter) WriteHeader(status int) {
	// Informational statuses precede the final header and decide nothing.
	if !w.decided && status >= http.StatusOK {
		w.decided = true
		h := w.Header()
		skipped := w.c.skipResponse != nil && w.c.skipResponse(w.r, h)
		if !skipped && w.c.negotiates(status, h) {
			h.Add("Vary", "Accept-Encoding")
			if w.encodes(h) {
				h.Set("Content-Encoding", w.coding.name)
				if n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64); err == nil && n >= 0 && n <= maxHeldLength {
					w.hold(status, n)
					return
				}
				h.Del("Content-Length")
				w.enc = w.coding.get(w.ResponseWriter)
			}
		}
	} else if w.holding() {
		// net/http would keep the first final status too.
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

// hold defers the response to close. The header is copied now so that, as
// with net/http, a change the handler makes after WriteHeader is not sent.
func (w *compressWriter) hold(status int, declared int64) {
	b, ok := heldBodies.Get().(*heldBody)
	if !ok {
		b = new(heldBody)
	}
	w.held, w.status, w.header, w.remaining = b, status, w.Header().Clone(), declared
	w.enc = w.coding.get(b)
}

func (w *compressWriter) holding() bool { return w.held != nil && w.held.out == nil }

// writeHeldHeader writes the held status and header, with Content-Length set
// to length, or removed when length is negative.
func (w *compressWriter) writeHeldHeader(length int) {
	h := w.Header()
	clear(h)
	maps.Copy(h, w.header)
	if length < 0 {
		h.Del("Content-Length")
	} else {
		h.Set("Content-Length", strconv.Itoa(length))
	}
	w.ResponseWriter.WriteHeader(w.status)
}

// stream ends a hold early: the header goes out without a length, then what
// the encoder has produced so far, and the rest passes straight through.
func (w *compressWriter) stream() error {
	if !w.holding() {
		return nil
	}
	w.writeHeldHeader(-1)
	w.held.out = w.ResponseWriter
	if w.held.buf.Len() == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(w.held.buf.Bytes())
	w.held.buf.Reset()
	return err
}

func (w *compressWriter) Write(p []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.enc != nil {
		if w.holding() {
			// A body longer than its declared length streams, so the hold
			// stays bounded by maxHeldLength.
			if int64(len(p)) > w.remaining {
				if err := w.stream(); err != nil {
					return 0, err
				}
			}
			w.remaining -= int64(len(p))
		}
		return w.enc.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// Flush pushes what the encoder holds to the client, which ends a hold. Like
// net/http, a flush before any write commits a 200.
func (w *compressWriter) Flush() {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.enc != nil {
		_ = w.stream()
		_ = w.enc.Flush()
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack is what the WebSocket upgrader asserts before it has written
// anything: gorilla answers 500 when the writer it is handed lacks it.
// io.ReaderFrom is deliberately absent: an encoded body cannot use sendfile,
// and the bulk routes that rely on it bypass the compressor through
// skipRequest.
func (w *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *compressWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// Unwrap lets http.ResponseController reach the connection's deadlines.
func (w *compressWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// close ends the encoded body, returns the encoder to its pool, and sends a
// held response with its encoded length. A client that has gone makes the
// final write fail, which nobody can act on.
func (w *compressWriter) close() {
	if w.enc == nil {
		return
	}
	_ = w.enc.Close()
	w.coding.put(w.enc)
	w.enc = nil
	b := w.held
	if b == nil {
		return
	}
	if b.out == nil {
		w.writeHeldHeader(b.buf.Len())
		_, _ = w.ResponseWriter.Write(b.buf.Bytes())
	}
	w.held, w.header = nil, nil
	// The pooled encoder still points at b until its next Reset, so a large
	// buffer is dropped rather than left reachable through it.
	if b.buf.Cap() > maxPooledHeld {
		b.buf = bytes.Buffer{}
	} else {
		b.buf.Reset()
	}
	b.out = nil
	heldBodies.Put(b)
}

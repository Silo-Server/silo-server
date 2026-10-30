package httpstream

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// A handler behind the response-time exclusion writer must still be able to
// hijack the connection: WebSocket upgraders assert http.Hijacker on the
// writer they are handed, and the wrapper sits in front of every response on
// the API listener.
func TestCompressWithExclusionsWriterSupportsHijack(t *testing.T) {
	const reply = "HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhijacked"
	h := CompressWithExclusions(nil, func(*http.Request, http.Header) bool { return false })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("writer %T does not implement http.Hijacker", w)
				http.Error(w, "no hijacker", http.StatusInternalServerError)
				return
			}
			conn, rw, err := hj.Hijack()
			if err != nil {
				t.Errorf("Hijack: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = rw.WriteString(reply)
			_ = rw.Flush()
		}))
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hijacked" {
		t.Fatalf("hijacked reply: %d %q", resp.StatusCode, body)
	}
}

// Flush still reaches the connection through the wrapper, before and after
// the exclusion decision, and what the handler wrote before a flush decodes
// before the body ends, which progressive subtitle extracts rely on.
func TestCompressWithExclusionsWriterSupportsFlush(t *testing.T) {
	for _, tc := range []struct {
		accept string
		skip   bool
	}{{"gzip", false}, {"br", false}, {"zstd", false}, {"gzip", true}} {
		rec := httptest.NewRecorder()
		h := CompressWithExclusions(nil, func(*http.Request, http.Header) bool { return tc.skip }, "text/plain")(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				if _, ok := w.(http.Flusher); !ok {
					t.Errorf("%s skip=%v: writer %T does not implement http.Flusher", tc.accept, tc.skip, w)
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("%s skip=%v: Flush before write: %v", tc.accept, tc.skip, err)
				}
				_, _ = io.WriteString(w, "chunk")
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("%s skip=%v: Flush after write: %v", tc.accept, tc.skip, err)
				}
				if got, _ := decodeBody(rec.Header().Get("Content-Encoding"), rec.Body.Bytes()); got != "chunk" {
					t.Errorf("%s skip=%v: flushed bytes decode to %q before the body ends", tc.accept, tc.skip, got)
				}
				_, _ = io.WriteString(w, " and the rest")
			}))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", tc.accept)
		h.ServeHTTP(rec, r)
		if !rec.Flushed {
			t.Errorf("%s skip=%v: recorder was not flushed", tc.accept, tc.skip)
		}
		if got, err := decodeBody(rec.Header().Get("Content-Encoding"), rec.Body.Bytes()); err != nil || got != "chunk and the rest" {
			t.Errorf("%s skip=%v: body decodes to %q, %v", tc.accept, tc.skip, got, err)
		}
	}
}

// The compressor applies the coding the client weights highest, ties going to
// zstd, then br, then gzip, marks every negotiated response Vary, and leaves
// excluded responses identity-encoded without it.
func TestCompressWithExclusionsNegotiatesCoding(t *testing.T) {
	var large strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&large, `{"content_id":"movie:%d","title":"Title %d","year":%d},`, i*7919, i, 1950+i%70)
	}
	small := `{"type":"not_found","status":404}`
	problem := `{"type":"https://example.test/problems/not_found","title":"Not found","status":404,"detail":"No catalog item has this id, or the profile cannot see it.","instance":"urn:silo:request:0123456789abcdef01234567","errors":[{"location":"path.id","code":"not_found","detail":"no such item"}]}`
	for _, tc := range []struct {
		name, accept, path, contentType string
		status                          int
		etag, noVary                    bool
		body                            string
		want                            string
	}{
		{name: "browser over HTTPS", accept: "gzip, deflate, br, zstd", want: "zstd"},
		{name: "URLSession", accept: "gzip, deflate, br", want: "br"},
		{name: "brotli body under one block", accept: "br", body: problem, want: "br"},
		{name: "body under the minimum length", accept: "gzip, br, zstd", body: small},
		{name: "short body when identity is refused", accept: "gzip, identity;q=0", body: small, want: "gzip"},
		{name: "short body when the wildcard refuses identity", accept: "br, *;q=0", body: small, want: "br"},
		{name: "OkHttp", accept: "gzip", want: "gzip"},
		{name: "x-gzip alias in any case", accept: "X-GZIP", want: "gzip"},
		{name: "weights beat server preference", accept: "zstd;q=0.5, br;q=0.8, gzip", want: "gzip"},
		{name: "q=0 refuses a coding", accept: "zstd;q=0, br, gzip", want: "br"},
		{name: "wildcard", accept: "*", want: "zstd"},
		{name: "wildcard covers only unnamed codings", accept: "zstd;q=0, *;q=0.5", want: "br"},
		{name: "wildcard below a named coding", accept: "*;q=0.2, gzip;q=0.5", want: "gzip"},
		{name: "malformed weights are ignored", accept: "br;q=2, gzip;q=x, zstd;q=0.1", want: "zstd"},
		{name: "everything refused", accept: "*;q=0"},
		{name: "identity only", accept: "identity"},
		{name: "identity weighted above every coding", accept: "identity;q=1, gzip;q=0.1"},
		{name: "identity tied with a coding", accept: "gzip, identity", want: "gzip"},
		{name: "no Accept-Encoding"},
		{name: "unsupported codings", accept: "deflate, compress"},
		{name: "problem documents", accept: "zstd", contentType: "application/problem+json", status: http.StatusNotFound, body: problem, want: "zstd"},
		{name: "incompressible type", accept: "gzip", contentType: "image/webp", noVary: true},
		{name: "partial content", accept: "gzip", contentType: "text/vtt", status: http.StatusPartialContent, noVary: true},
		{name: "request exclusion", accept: "gzip", path: "/media", noVary: true},
		{name: "validator-bearing response", accept: "zstd, br, gzip", etag: true, noVary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := cmp.Or(tc.body, large.String())
			h := CompressWithExclusions(
				func(r *http.Request) bool { return r.URL.Path == "/media" },
				func(_ *http.Request, h http.Header) bool { return h.Get("ETag") != "" },
			)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", cmp.Or(tc.contentType, "application/json; charset=utf-8"))
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				if tc.etag {
					w.Header().Set("ETag", `"v1"`)
				}
				if tc.status == http.StatusPartialContent {
					w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(body)-1, len(body)+10))
				}
				w.WriteHeader(cmp.Or(tc.status, http.StatusOK))
				_, _ = io.WriteString(w, body)
			}))
			r := httptest.NewRequest(http.MethodGet, cmp.Or(tc.path, "/api"), nil)
			if tc.accept != "" {
				r.Header.Set("Accept-Encoding", tc.accept)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			got := rec.Header().Get("Content-Encoding")
			if got != tc.want {
				t.Fatalf("Content-Encoding = %q, want %q", got, tc.want)
			}
			wantVary, wantLength := "Accept-Encoding", strconv.Itoa(len(body))
			if tc.noVary {
				wantVary = ""
			}
			if tc.want != "" {
				wantLength = ""
			}
			if vary, length := rec.Header().Get("Vary"), rec.Header().Get("Content-Length"); vary != wantVary || length != wantLength {
				t.Fatalf("Vary = %q, Content-Length = %q; want %q, %q", vary, length, wantVary, wantLength)
			}
			decoded, err := decodeBody(got, rec.Body.Bytes())
			if err != nil || decoded != body {
				t.Fatalf("body does not decode to the handler's (%d of %d bytes): %v", len(decoded), len(body), err)
			}
		})
	}
}

// decodeBody decodes b in the given content coding, returning what it decoded
// before any error so a stream that has not ended yet can be checked.
func decodeBody(coding string, b []byte) (string, error) {
	var r io.Reader
	switch coding {
	case "":
		return string(b), nil
	case codingGzip:
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return "", err
		}
		r = zr
	case codingBrotli:
		r = brotli.NewReader(bytes.NewReader(b))
	case codingZstd:
		zr, err := zstd.NewReader(bytes.NewReader(b))
		if err != nil {
			return "", err
		}
		defer zr.Close()
		r = zr
	default:
		return "", fmt.Errorf("unexpected coding %q", coding)
	}
	out, err := io.ReadAll(r)
	return string(out), err
}

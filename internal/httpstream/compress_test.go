package httpstream

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A handler behind the response-time exclusion writer must still be able to
// hijack the connection: WebSocket upgraders assert http.Hijacker on the
// writer they are handed, and the wrapper sits in front of every response on
// the API listener.
func TestCompressWithExclusionsWriterSupportsHijack(t *testing.T) {
	const reply = "HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhijacked"
	h := CompressWithExclusions(5, nil, func(*http.Request, http.Header) bool { return false })(
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
// the exclusion decision.
func TestCompressWithExclusionsWriterSupportsFlush(t *testing.T) {
	for _, skip := range []bool{false, true} {
		h := CompressWithExclusions(5, nil, func(*http.Request, http.Header) bool { return skip }, "text/plain")(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				if _, ok := w.(http.Flusher); !ok {
					t.Errorf("skip=%v: writer %T does not implement http.Flusher", skip, w)
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("skip=%v: Flush before write: %v", skip, err)
				}
				_, _ = io.WriteString(w, "chunk")
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("skip=%v: Flush after write: %v", skip, err)
				}
			}))
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(rec, r)
		if !rec.Flushed {
			t.Errorf("skip=%v: recorder was not flushed", skip)
		}
	}
}

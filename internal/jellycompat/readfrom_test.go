package jellycompat

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type readerFromSpy struct {
	bytes.Buffer
	header http.Header
	calls  int
}

func (w *readerFromSpy) Header() http.Header { return w.header }
func (w *readerFromSpy) WriteHeader(int)     {}
func (w *readerFromSpy) ReadFrom(r io.Reader) (int64, error) {
	w.calls++
	return io.Copy(&w.Buffer, r)
}

func TestResponseWritersPreserveReaderFrom(t *testing.T) {
	for _, tt := range []struct {
		name string
		new  func(*readerFromSpy) io.ReaderFrom
	}{
		{"request status", func(spy *readerFromSpy) io.ReaderFrom { return &statusResponseWriter{ResponseWriter: spy} }},
		{"debug media", func(spy *readerFromSpy) io.ReaderFrom {
			spy.header.Set("Content-Type", "video/mp4")
			return &debugResponseWriter{ResponseWriter: spy}
		}},
		{"image proxy passthrough", func(spy *readerFromSpy) io.ReaderFrom {
			spy.header.Set("Content-Type", "video/mp4")
			return &compatImageProxyTagResponseWriter{ResponseWriter: spy}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spy := &readerFromSpy{header: make(http.Header)}
			n, err := tt.new(spy).ReadFrom(bytes.NewBufferString("media"))
			if err != nil || n != 5 || spy.calls != 1 || spy.String() != "media" {
				t.Fatalf("ReadFrom = n=%d err=%v calls=%d body=%q", n, err, spy.calls, spy.String())
			}
		})
	}
}

func TestDebugLogMiddlewareReadFromLogsTextAndMedia(t *testing.T) {
	for _, tt := range []struct {
		name, contentType, body, want string
	}{
		{"json", "application/json", `{"ok":true}`, `Response (content-type=application/json, 11 bytes)`},
		{"media", "video/mp4", "media", `Response: [binary content-type=video/mp4 bytes=5]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			h := newDebugLogMiddleware(&log, "")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = io.Copy(w, struct{ io.Reader }{bytes.NewBufferString(tt.body)})
			}))
			spy := &readerFromSpy{header: make(http.Header)}
			h.ServeHTTP(spy, httptest.NewRequest(http.MethodGet, "/", nil))
			if spy.String() != tt.body {
				t.Fatalf("forwarded body = %q, want %q", spy.String(), tt.body)
			}
			if !strings.Contains(log.String(), tt.want) {
				t.Fatalf("debug log = %q, want %q", log.String(), tt.want)
			}
			if tt.name == "json" && !strings.Contains(log.String(), `"ok": true`) {
				t.Fatalf("JSON body missing from debug log: %q", log.String())
			}
			if tt.name == "media" && spy.calls != 1 {
				t.Fatalf("media ReadFrom calls = %d, want 1", spy.calls)
			}
			if tt.name == "media" && strings.Contains(log.String(), "media\n") {
				t.Fatalf("binary body leaked into debug log: %q", log.String())
			}
		})
	}
}

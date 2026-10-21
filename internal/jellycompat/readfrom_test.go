package jellycompat

import (
	"bytes"
	"io"
	"net/http"
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

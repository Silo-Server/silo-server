package middleware

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

func TestStatusWritersPreserveReaderFrom(t *testing.T) {
	for _, wrap := range []struct {
		name string
		new  func(http.ResponseWriter) io.ReaderFrom
	}{
		{"request logger", func(w http.ResponseWriter) io.ReaderFrom { return &requestStatusWriter{ResponseWriter: w} }},
		{"metrics", func(w http.ResponseWriter) io.ReaderFrom { return &statusWriter{ResponseWriter: w} }},
	} {
		t.Run(wrap.name, func(t *testing.T) {
			spy := &readerFromSpy{header: make(http.Header)}
			n, err := wrap.new(spy).ReadFrom(bytes.NewBufferString("media"))
			if err != nil || n != 5 || spy.calls != 1 || spy.String() != "media" {
				t.Fatalf("ReadFrom = n=%d err=%v calls=%d body=%q", n, err, spy.calls, spy.String())
			}
		})
	}
}

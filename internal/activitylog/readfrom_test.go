package activitylog

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

type readerFromSpy struct{ bytes.Buffer }

func (w *readerFromSpy) Header() http.Header { return make(http.Header) }
func (w *readerFromSpy) WriteHeader(int)     {}
func (w *readerFromSpy) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(&w.Buffer, r)
}

func TestStatusWriterPreservesReaderFrom(t *testing.T) {
	spy := &readerFromSpy{}
	w := &statusWriter{ResponseWriter: spy}
	n, err := w.ReadFrom(bytes.NewBufferString("media"))
	if err != nil || n != 5 || spy.String() != "media" || w.status != http.StatusOK {
		t.Fatalf("ReadFrom = n=%d err=%v status=%d body=%q", n, err, w.status, spy.String())
	}
}

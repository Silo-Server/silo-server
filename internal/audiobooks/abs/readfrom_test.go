package abs

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

func TestStatusRecorderPreservesReaderFromAndAccounting(t *testing.T) {
	spy := &readerFromSpy{}
	w := &statusRecorder{ResponseWriter: spy}
	n, err := w.ReadFrom(bytes.NewBufferString("media"))
	if err != nil || n != 5 || w.bytes != 5 || w.status != http.StatusOK || spy.String() != "media" {
		t.Fatalf("ReadFrom = n=%d err=%v status=%d bytes=%d body=%q", n, err, w.status, w.bytes, spy.String())
	}
	if w.Unwrap() != spy {
		t.Fatal("Unwrap did not return underlying writer")
	}
}

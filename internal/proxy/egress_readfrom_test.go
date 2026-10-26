package proxy

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

type egressReaderFromSpy struct{ bytes.Buffer }

func (w *egressReaderFromSpy) Header() http.Header { return make(http.Header) }
func (w *egressReaderFromSpy) WriteHeader(int)     {}
func (w *egressReaderFromSpy) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(&w.Buffer, r)
}

// A slice credits the meter only when it completes, so the slice has to be small
// enough that a slow viewer still registers inside the 60 s rate window. This
// pins the granularity: a transfer the size of one shared 4 MiB default slice
// must produce several credits, not one.
func TestMeteredResponseWriterReadFromCreditsIncrementally(t *testing.T) {
	spy := &egressReaderFromSpy{}
	meter := newEgressMeter()
	credits := 0
	meter.now = func() time.Time {
		// Add consults the clock exactly once per credit, and returns before
		// doing so for a zero-byte slice, so this counts credits.
		credits++
		return time.Unix(1_000_000, 0)
	}
	w := &meteredResponseWriter{ResponseWriter: spy, meter: meter}

	const body = httpstream.ReadFromChunkDefault
	n, err := w.ReadFrom(bytes.NewReader(make([]byte, body)))
	if err != nil || n != body || len(spy.Bytes()) != int(body) {
		t.Fatalf("ReadFrom = n=%d err=%v body=%d", n, err, len(spy.Bytes()))
	}
	wantCredits := int(body / meterChunk)
	if credits != wantCredits {
		t.Fatalf("meter credited %d times over %d bytes, want %d (one per %d-byte slice)",
			credits, body, wantCredits, meterChunk)
	}
	if got := meter.RateKbps(); got <= 0 {
		t.Fatalf("meter rate = %d, want > 0", got)
	}
}

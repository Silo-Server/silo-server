package proxy

import (
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// meterWindowSeconds is the averaging window for the egress rate. HLS clients
// fetch segments in bursts (especially when buffering ahead), so a window of
// this size smooths the spikes into something close to the steady-state
// stream rate. The planner's bandwidth reservation bridge matches this value.
const meterWindowSeconds = 60

// egressMeter measures outbound stream bytes as a rolling per-second ring,
// reporting the average rate over the window. Safe for concurrent use.
type egressMeter struct {
	mu sync.Mutex
	// One extra bucket so the current (partial) second never collides with
	// the oldest second still inside the window.
	buckets [meterWindowSeconds + 1]int64
	stamps  [meterWindowSeconds + 1]int64 // unix second each bucket holds
	now     func() time.Time
}

func newEgressMeter() *egressMeter {
	return &egressMeter{now: time.Now}
}

// Add records n egressed bytes against the current second.
func (m *egressMeter) Add(n int64) {
	if n <= 0 {
		return
	}
	sec := m.now().Unix()
	i := int(sec % int64(len(m.buckets)))
	m.mu.Lock()
	if m.stamps[i] != sec {
		m.stamps[i] = sec
		m.buckets[i] = 0
	}
	m.buckets[i] += n
	m.mu.Unlock()
}

// RateKbps returns the average egress over the window in kilobits/s.
func (m *egressMeter) RateKbps() int {
	sec := m.now().Unix()
	var total int64
	m.mu.Lock()
	for i := range m.buckets {
		if sec-m.stamps[i] < meterWindowSeconds {
			total += m.buckets[i]
		}
	}
	m.mu.Unlock()
	return int(total * 8 / 1000 / meterWindowSeconds)
}

// meterChunk bounds one zero-copy slice on a metered response.
//
// This is a rate-fidelity constraint, not a tuning knob. The meter is a
// per-second ring averaged over 60 s, and a slice credits it only when the slice
// completes, so the slice has to be short relative to that window at the SLOWEST
// rate worth measuring. At the shared 4 MiB default a 200-500 kbit/s viewer
// takes 60-170 s per slice: RateKbps reads that stream as zero for most samples,
// /api/v1/status under-reports committed egress, and the planner's
// effectiveEgressKbps can admit new sessions onto a saturated proxy. 256 KiB
// credits the same viewer roughly every 4-10 s, well inside the window, while
// still handing the kernel 8x more per sendfile call than the 32 KiB Write path
// this replaced.
const meterChunk int64 = 256 << 10

// meteredResponseWriter counts every byte written to the client. Chunked
// ReaderFrom delegation preserves both sendfile and the rolling rate window.
type meteredResponseWriter struct {
	http.ResponseWriter
	meter *egressMeter
}

func (w *meteredResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.meter.Add(int64(n))
	return n, err
}

func (w *meteredResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	return httpstream.ForwardReadFrom(w.ResponseWriter, w, src, meterChunk, func(n int64, _ error) {
		w.meter.Add(n)
	})
}

func (w *meteredResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController traverse to the underlying writer.
//
// Without it the metering wrapper is a dead end: RollingDeadlineWriter's
// SetWriteDeadline call fails, it degrades to a plain pass-through, and since
// the standalone proxy runs with WriteTimeout 0 there is no server-level guard
// behind it. A client that stops reading without closing its connection would
// then block a stream write forever, holding the tracked session, the open
// file, the goroutine and the connection.
func (w *meteredResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// meterEgress wraps stream handlers so their responses count toward the
// node's measured egress bandwidth.
func (s *Server) meterEgress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&meteredResponseWriter{ResponseWriter: w, meter: s.egress}, r)
	})
}

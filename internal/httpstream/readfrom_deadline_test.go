package httpstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// pacedReader delivers src at a fixed byte rate, so a transfer takes a
// predictable wall-clock time regardless of the caller's read-buffer size. It
// models a slow disk or a rate-limited upstream, which is what makes a single
// zero-copy slice long-lived.
type pacedReader struct {
	remaining int64
	piece     int64
	pause     time.Duration
	started   time.Time
	delivered int64
}

func (r *pacedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := r.piece
	if n > int64(len(p)) {
		n = int64(len(p))
	}
	if n > r.remaining {
		n = r.remaining
	}
	// ReaderFrom implementations choose their own buffer size. Scale the pause
	// to the bytes actually returned instead of assuming every call accepts a
	// full piece; otherwise a smaller platform buffer silently slows the reader
	// and consumes the deadline margin this test is meant to control.
	if r.started.IsZero() {
		r.started = time.Now()
	}
	r.delivered += n
	time.Sleep(time.Until(r.started.Add(time.Duration(r.delivered) * r.pause / time.Duration(r.piece))))
	r.remaining -= n
	return int(n), nil
}

// sliceDuration is how long one readFromChunk-sized slice takes to be produced
// by the pacedReader configured below. The tests derive their stall windows from
// it so they stay correct if readFromChunk changes.
const (
	testPiece      = 64 << 10
	testPiecePause = 2 * time.Millisecond
)

func sliceDuration() time.Duration {
	return time.Duration(readFromChunk/testPiece) * testPiecePause
}

// TestReadFromChunkAllowsSlowClients guards the constant itself. The reap
// threshold is readFromChunk / DefaultStallWindow: any client sustaining less
// than that is killed mid-slice despite healthy progress. 64 MiB over the 180s
// default worked out to ~3 Mbit/s, which reaps ordinary mobile connections.
func TestReadFromChunkAllowsSlowClients(t *testing.T) {
	const maxAcceptableFloorBitsPerSec = 256 << 10 // 256 kbit/s

	floor := float64(readFromChunk) * 8 / DefaultStallWindow.Seconds()
	if floor > maxAcceptableFloorBitsPerSec {
		t.Fatalf("readFromChunk %d over a %s window reaps clients below %.0f bit/s; keep it under %d bit/s",
			readFromChunk, DefaultStallWindow, floor, maxAcceptableFloorBitsPerSec)
	}
}

// TestReadFromRollsDeadlineUnderProductionStep is the same guarantee with the
// real bumpStep in play. The other deadline tests construct the writer with
// step=0, so they never exercise the throttle — and the throttle was the bug:
// a slice completing less than a step after the last bump got no refresh, so the
// next slice started with as little as window-step remaining and a client
// sustaining the documented floor rate was reaped despite never stalling.
func TestReadFromRollsDeadlineUnderProductionStep(t *testing.T) {
	slice := sliceDuration()
	window := slice * 6
	total := readFromChunk * 8

	done := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A step far longer than the whole transfer: with the throttle applied to
		// slices, every bump after the first would be suppressed.
		sw := newRollingDeadlineWriter(w, window, time.Hour)
		sw.WriteHeader(http.StatusOK)
		_, err := sw.ReadFrom(&pacedReader{remaining: total, piece: testPiece, pause: testPiecePause})
		done <- err
	}))
	srv.Config.WriteTimeout = 0
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		t.Fatalf("stream died after %d/%d bytes under the production bump step: %v", n, total, err)
	}
	if n != total {
		t.Fatalf("short body: got %d bytes, want %d", n, total)
	}
	if handlerErr := <-done; handlerErr != nil {
		t.Fatalf("handler ReadFrom returned %v; the step throttle must not shorten a slice's window", handlerErr)
	}
}

// A handler that commits headers and then waits before its first write must not
// spend that wait against the window set at construction.
func TestReadFromBumpsBeforeTheFirstSlice(t *testing.T) {
	slice := sliceDuration()
	window := slice * 6
	total := readFromChunk

	done := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sw := newRollingDeadlineWriter(w, window, time.Hour)
		sw.WriteHeader(http.StatusOK)
		// Stand in for waiting on artifact readiness: longer than the window, so
		// only a bump before the first slice can save the transfer.
		time.Sleep(window + 50*time.Millisecond)
		_, err := sw.ReadFrom(&pacedReader{remaining: total, piece: testPiece, pause: testPiecePause})
		done <- err
	}))
	srv.Config.WriteTimeout = 0
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil || n != total {
		t.Fatalf("first slice ran against a stale deadline: %d/%d bytes, err %v", n, total, err)
	}
	if handlerErr := <-done; handlerErr != nil {
		t.Fatalf("handler ReadFrom returned %v after a long pre-write wait", handlerErr)
	}
}

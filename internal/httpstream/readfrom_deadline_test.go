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

// TestReadFromRollsDeadlineBetweenSlices is the regression test for the reap of
// healthy-but-slow streams. The write deadline is an absolute time, so any write
// attempted after it fails immediately: a transfer that outlives the stall window
// survives only because the deadline is pushed forward *between* slices. Before
// the slice was reduced to ReadFromChunkDefault, a single 64 MiB slice at a
// modest rate outlasted the whole window and the stream was reaped mid-transfer
// despite making continuous progress.
func TestReadFromRollsDeadlineBetweenSlices(t *testing.T) {
	slice := sliceDuration()
	// Leave enough headroom for scheduler jitter while keeping the whole
	// transfer longer than the window, so only per-slice bumping can carry it to
	// completion.
	window := slice * 6
	total := readFromChunk * 8

	done := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sw := newRollingDeadlineWriter(w, window, 0 /* bump every slice */)
		sw.WriteHeader(http.StatusOK)
		_, err := sw.ReadFrom(&pacedReader{remaining: total, piece: testPiece, pause: testPiecePause})
		done <- err
	}))
	srv.Config.WriteTimeout = 0 // isolate: only the rolling deadline may reap
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		t.Fatalf("slow but continuously progressing stream died after %d/%d bytes: %v", n, total, err)
	}
	if n != total {
		t.Fatalf("short body: got %d bytes, want %d", n, total)
	}
	if handlerErr := <-done; handlerErr != nil {
		t.Fatalf("handler ReadFrom returned %v; a steadily progressing stream must not be reaped", handlerErr)
	}
}

// TestOversizedReadFromSliceIsReaped pins down *why* the slice size matters: with
// a slice long enough to outlast the stall window, the deadline set before it
// expires part-way through and the transfer dies even though it never stopped
// making progress. This is the behavior the 64 MiB default produced, and it must
// stay reproducible so nobody restores a large slice without noticing.
func TestOversizedReadFromSliceIsReaped(t *testing.T) {
	slice := sliceDuration()
	window := slice * 6
	oversized := readFromChunk * 12 // one slice ≈ 12x slice duration >> window

	done := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sw := newRollingDeadlineWriter(w, window, 0)
		sw.WriteHeader(http.StatusOK)
		rf, ok := ReaderFromOf(sw.w)
		if !ok {
			done <- nil
			t.Error("test server ResponseWriter does not implement io.ReaderFrom")
			return
		}
		// Deliberately drive a single oversized slice: no bump can happen inside it.
		_, err := CopyChunked(rf, &pacedReader{remaining: oversized, piece: testPiece, pause: testPiecePause}, oversized, nil)
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
	_, _ = io.Copy(io.Discard, resp.Body)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a slice longer than the stall window completed; the deadline is no longer enforced mid-slice")
		}
		if !isTimeoutError(err) {
			t.Fatalf("oversized slice failed with %v, want a deadline timeout", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("oversized slice never returned")
	}
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

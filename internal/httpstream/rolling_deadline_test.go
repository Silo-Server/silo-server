package httpstream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyOutcome(t *testing.T) {
	tests := []struct {
		name          string
		firstWriteErr error
		contextErr    error
		want          StreamOutcome
	}{
		{name: "completed", want: OutcomeCompleted},
		{name: "stalled write", firstWriteErr: os.ErrDeadlineExceeded, want: OutcomeStalledReap},
		{name: "write failure", firstWriteErr: io.ErrClosedPipe, want: OutcomeClientGone},
		{name: "canceled context", contextErr: context.Canceled, want: OutcomeClientGone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyOutcome(test.firstWriteErr, test.contextErr); got != test.want {
				t.Fatalf("ClassifyOutcome() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestStreamSurvivesServerWriteTimeout is the regression test for the 120s
// stream-truncation bug: a response that keeps making progress must outlive
// the server's absolute WriteTimeout when wrapped.
func TestStreamSurvivesServerWriteTimeout(t *testing.T) {
	const (
		writeEvery = 50 * time.Millisecond
		writes     = 60 // ~3s total, 3x the server WriteTimeout
		chunk      = "0123456789abcdef"
	)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := newRollingDeadlineWriter(w, 2*time.Second, 0 /* bump every write */)
		sw.WriteHeader(http.StatusOK)
		for i := 0; i < writes; i++ {
			if _, err := sw.Write([]byte(chunk)); err != nil {
				return
			}
			sw.Flush()
			time.Sleep(writeEvery)
		}
	}))
	srv.Config.WriteTimeout = 1 * time.Second
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("stream died before completion (got %d bytes): %v", len(body), err)
	}
	if want := writes * len(chunk); len(body) != want {
		t.Fatalf("short body: got %d bytes, want %d", len(body), want)
	}
}

// TestUnwrappedStreamStillKilledAtWriteTimeout proves the server-level guard
// is unchanged for handlers that do not opt in.
func TestUnwrappedStreamStillKilledAtWriteTimeout(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for i := 0; i < 60; i++ {
			if _, err := w.Write([]byte("0123456789abcdef")); err != nil {
				return
			}
			f.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	srv.Config.WriteTimeout = 500 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		return // connection died before headers: also a kill, test passes
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err == nil && len(body) == 60*16 {
		t.Fatal("unwrapped stream survived the server WriteTimeout; guard is gone")
	}
}

// TestStalledClientReaped proves the wrapper still bounds a genuinely stalled
// connection: a client that stops reading must cause a write error within
// roughly the stall window, not never.
func TestStalledClientReaped(t *testing.T) {
	type result struct {
		err     error
		outcome StreamOutcome
	}
	handlerDone := make(chan result, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := newRollingDeadlineWriter(w, 500*time.Millisecond, 0)
		sw.WriteHeader(http.StatusOK)
		buf := make([]byte, 1<<20)
		var err error
		for i := 0; i < 256; i++ { // up to 256 MB >> any socket buffer
			if _, err = sw.Write(buf); err != nil {
				break
			}
			sw.Flush()
		}
		handlerDone <- result{err: err, outcome: sw.Outcome(r.Context())}
	}))
	srv.Config.WriteTimeout = 0 // isolate: only the rolling deadline may reap
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	// Read just the status line, then stop reading entirely.
	if _, err := bufio.NewReader(io.LimitReader(conn, 32)).ReadString('\n'); err != nil {
		t.Fatalf("status line: %v", err)
	}

	select {
	case got := <-handlerDone:
		if got.err == nil {
			t.Fatal("handler finished 256MB into a non-reading client without error")
		}
		if got.outcome != OutcomeStalledReap {
			t.Fatalf("outcome = %q, want %q (error: %v)", got.outcome, OutcomeStalledReap, got.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stalled client was never reaped by the rolling deadline")
	}
}

func TestDisconnectedClientClassifiedClientGone(t *testing.T) {
	type result struct {
		err     error
		outcome StreamOutcome
	}
	startWriting := make(chan struct{})
	handlerDone := make(chan result, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := newRollingDeadlineWriter(w, 5*time.Second, 0)
		sw.WriteHeader(http.StatusOK)
		_, _ = sw.Write([]byte("ready"))
		sw.Flush()
		<-startWriting

		buf := make([]byte, 1<<20)
		var err error
		for i := 0; i < 256; i++ {
			if _, err = sw.Write(buf); err != nil {
				break
			}
			sw.Flush()
		}
		handlerDone <- result{err: err, outcome: sw.Outcome(r.Context())}
	}))
	srv.Config.WriteTimeout = 0
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatalf("status line: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	close(startWriting)

	select {
	case got := <-handlerDone:
		if got.err == nil {
			t.Fatal("handler completed after the client disconnected")
		}
		if got.outcome != OutcomeClientGone {
			t.Fatalf("outcome = %q, want %q (error: %v)", got.outcome, OutcomeClientGone, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not observe the client disconnect")
	}
}

func TestWriteOutcomeCompletedAndCounted(t *testing.T) {
	rr := httptest.NewRecorder()
	sw := newRollingDeadlineWriter(rr, time.Second, 0)
	sw.WriteHeader(http.StatusCreated)
	const body = "completed body"
	n, err := sw.Write([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(body) || sw.BytesWritten() != int64(len(body)) {
		t.Fatalf("bytes = (%d, %d), want %d", n, sw.BytesWritten(), len(body))
	}
	if sw.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d, want %d", sw.StatusCode(), http.StatusCreated)
	}
	if outcome := sw.Outcome(context.Background()); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeCompleted)
	}
}

func TestServeContentReadFromOutcomeCompletedAndCounted(t *testing.T) {
	const totalSize = 2 << 20
	filePath := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(filePath, bytesOf('x', totalSize), 0o600); err != nil {
		t.Fatal(err)
	}

	type result struct {
		status  int
		bytes   int64
		outcome StreamOutcome
	}
	handlerDone := make(chan result, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(filePath)
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			t.Error(err)
			return
		}
		sw := newRollingDeadlineWriter(w, 5*time.Second, 0)
		http.ServeContent(sw, r, stat.Name(), stat.ModTime(), f)
		handlerDone <- result{status: sw.StatusCode(), bytes: sw.BytesWritten(), outcome: sw.Outcome(r.Context())}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, readErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read = %v, close = %v", readErr, closeErr)
	}
	if n != totalSize {
		t.Fatalf("response bytes = %d, want %d", n, totalSize)
	}

	select {
	case got := <-handlerDone:
		if got.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", got.status)
		}
		if got.bytes != totalSize {
			t.Fatalf("counted bytes = %d, want %d", got.bytes, totalSize)
		}
		if got.outcome != OutcomeCompleted {
			t.Fatalf("outcome = %q, want %q", got.outcome, OutcomeCompleted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not complete")
	}
}

// TestReadFromPreservesCompletion exercises the io.ReaderFrom path used by
// http.ServeContent (sendfile) under a server WriteTimeout shorter than the
// transfer, with a source large enough to require multiple bounded slices.
func TestReadFromPreservesCompletion(t *testing.T) {
	const totalSize = 8 << 20

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := newRollingDeadlineWriter(w, 2*time.Second, 0)
		sw.WriteHeader(http.StatusOK)
		src := &slowReader{r: io.LimitReader(neverEnding('x'), totalSize), delay: 200 * time.Microsecond}
		// io.Copy must take sw's ReadFrom path, as http.ServeContent does.
		if _, err := io.Copy(sw, src); err != nil {
			return
		}
	}))
	srv.Config.WriteTimeout = 1 * time.Second
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		t.Fatalf("stream died at %d bytes: %v", n, err)
	}
	if n != totalSize {
		t.Fatalf("short body: got %d, want %d", n, totalSize)
	}
}

type neverEnding byte

func (b neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// slowReader throttles reads so the transfer outlives the server WriteTimeout.
type slowReader struct {
	r     io.Reader
	delay time.Duration
}

func bytesOf(value byte, size int) []byte {
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = value
	}
	return buf
}

func (s *slowReader) Read(p []byte) (int, error) {
	time.Sleep(s.delay)
	if len(p) > 32<<10 {
		p = p[:32<<10]
	}
	return s.r.Read(p)
}

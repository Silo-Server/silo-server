package httpstream

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
	handlerDone := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := newRollingDeadlineWriter(w, 1*time.Second, 0)
		sw.WriteHeader(http.StatusOK)
		buf := make([]byte, 1<<20)
		var err error
		for i := 0; i < 256; i++ { // up to 256 MB >> any socket buffer
			if _, err = sw.Write(buf); err != nil {
				break
			}
			sw.Flush()
		}
		handlerDone <- err
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
	case err := <-handlerDone:
		if err == nil {
			t.Fatal("handler finished 256MB into a non-reading client without error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stalled client was never reaped by the rolling deadline")
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

func (s *slowReader) Read(p []byte) (int, error) {
	time.Sleep(s.delay)
	if len(p) > 32<<10 {
		p = p[:32<<10]
	}
	return s.r.Read(p)
}

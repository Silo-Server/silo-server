package playback

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// countingResponseWriter discards a streamed body and reports how much of it
// has been written, which is how a test observes that bytes are flowing without
// buffering a stream that never ends.
type countingResponseWriter struct {
	mu      sync.Mutex
	header  http.Header
	written int64
}

func (w *countingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *countingResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.written += int64(len(p))
	w.mu.Unlock()
	return len(p), nil
}

func (w *countingResponseWriter) WriteHeader(int) {}

func (w *countingResponseWriter) bytesWritten() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written
}

// streamingFakeFFmpegScript stands in for a remux: it writes to stdout forever
// and only ever stops when it is killed. Capability probes (`-bsfs`) answer
// immediately instead, so the serve path is not blocked before it starts.
func streamingFakeFFmpegScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"pipe:1\" ]; then\n" +
		"    while :; do printf '0123456789'; sleep 0.01; done\n" +
		"  fi\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForBytes(t *testing.T, w *countingResponseWriter) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for w.bytesWritten() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no remux bytes reached the response")
		}
		time.Sleep(time.Millisecond)
	}
}

// A progressive remux is one long response whose ffmpeg belongs to the request
// that started it. Withdrawing the route — a copy-safety verdict, an admin kill
// — only reaches the client if the stop can end the response itself.
func TestServeRemuxAbortEndsTheResponse(t *testing.T) {
	source := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(source, []byte("not really a movie"), 0o644); err != nil {
		t.Fatal(err)
	}

	abort := make(chan struct{})
	recorder := &countingResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/stream/session-1", nil)

	served := make(chan error, 1)
	go func() {
		served <- ServeRemuxWithOptions(recorder, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
			FFmpegPath: streamingFakeFFmpegScript(t),
			Abort:      abort,
		})
	}()

	waitForBytes(t, recorder)
	select {
	case err := <-served:
		t.Fatalf("the remux response ended on its own: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(abort)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ServeRemuxWithOptions after abort = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the remux response outlived the stop that withdrew its session")
	}
}

func TestWatchTransportStopSignalsOnStopSession(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	stop, release := sessions.WatchTransportStop(session.ID)
	defer release()

	select {
	case <-stop:
		t.Fatal("the transport was signaled while its session was still live")
	default:
	}

	if err := sessions.StopSession(session.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	select {
	case <-stop:
	case <-time.After(time.Second):
		t.Fatal("stopping the session did not signal its in-flight transport")
	}

	// A stop that already signaled must not be signaled again by the release
	// the serving handler runs on its way out; a second close would panic.
	release()
	release()
}

// Two transports can share a session — a client that reconnects while the old
// response is still draining — and a stop has to end both.
func TestWatchTransportStopSignalsEveryTransport(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	first, releaseFirst := sessions.WatchTransportStop(session.ID)
	defer releaseFirst()
	second, releaseSecond := sessions.WatchTransportStop(session.ID)
	defer releaseSecond()

	if err := sessions.StopSession(session.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	for i, stop := range []<-chan struct{}{first, second} {
		select {
		case <-stop:
		case <-time.After(time.Second):
			t.Fatalf("transport %d was left streaming after its session was stopped", i)
		}
	}
}

// The serving handler calls BeginTransport and WatchTransportStop as two
// separate steps. A stop landing between them used to register a watcher under
// an id nothing would ever signal again, and the progressive remux it guarded
// ran to EOF serving a route the server had already withdrawn. A watch for a
// session that is already gone reports the stop it missed.
func TestWatchTransportStopAfterStopSessionIsAlreadyClosed(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sessions.BeginTransport(session.ID); err != nil {
		t.Fatalf("BeginTransport: %v", err)
	}
	if err := sessions.StopSession(session.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	stop, release := sessions.WatchTransportStop(session.ID)
	select {
	case <-stop:
	default:
		t.Fatal("a transport registered after its session was stopped was left waiting for a signal that can never come")
	}

	// The release is a no-op for a watcher that was never registered, and must
	// stay safe to call from the serving handler's defer.
	release()
	release()
	sessions.mu.RLock()
	remaining := len(sessions.transportStops)
	sessions.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("transportStops holds %d sessions, want 0", remaining)
	}
}

// A transport that finished normally unregisters itself, so a later stop for
// the same session ID has nothing to signal and nothing to leak.
func TestWatchTransportStopReleaseUnregisters(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	stop, release := sessions.WatchTransportStop(session.ID)
	release()

	if err := sessions.StopSession(session.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	select {
	case <-stop:
		t.Fatal("a released transport was signaled")
	default:
	}
	sessions.mu.RLock()
	remaining := len(sessions.transportStops)
	sessions.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("transportStops holds %d sessions, want 0", remaining)
	}
}

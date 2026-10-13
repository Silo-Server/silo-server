package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// deliveryCounter serves handler behind the same middleware the stream routes
// use and counts the delivery records it would write for "session-1".
func deliveryCounter(t *testing.T, mark bool, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var recorded atomic.Int64
	record := func(sessionID string) {
		if sessionID != "session-1" {
			t.Errorf("recorded delivery for %q", sessionID)
		}
		recorded.Add(1)
	}
	server := httptest.NewServer(meterStream(newEgressMeter(), record, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mark {
			noteDelivery(r, &streamtoken.Claims{SessionID: "session-1"})
		}
		handler(w, r)
	})))
	t.Cleanup(server.Close)
	return server, &recorded
}

func requestDelivery(t *testing.T, server *httptest.Server, method string, header http.Header) int {
	t.Helper()
	req, err := http.NewRequest(method, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	// Receiving the body can precede the server's final delivery callback.
	// Close waits for the handler to finish before callers inspect its records.
	server.Close()
	return resp.StatusCode
}

// Only playback media the client actually receives keeps its session alive. A
// client that keeps asking for something it cannot get, or a preflight, must
// not hold the session and its stream slot.
func TestDeliveryCountsOnlyMediaTheClientReceives(t *testing.T) {
	media := bytes.Repeat([]byte("m"), 4096)
	serveMedia := func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "movie.mp4", time.Unix(1_700_000_000, 0), bytes.NewReader(media))
	}
	tests := []struct {
		name       string
		mark       bool
		method     string
		header     http.Header
		handler    http.HandlerFunc
		wantStatus int
		wantRecord bool
	}{
		{name: "full body", mark: true, method: http.MethodGet, handler: serveMedia, wantStatus: http.StatusOK, wantRecord: true},
		{name: "partial content", mark: true, method: http.MethodGet, header: http.Header{"Range": {"bytes=0-99"}}, handler: serveMedia, wantStatus: http.StatusPartialContent, wantRecord: true},
		{name: "body without explicit header", mark: true, method: http.MethodGet, handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "segment-bytes")
		}, wantStatus: http.StatusOK, wantRecord: true},
		{name: "unsatisfiable range", mark: true, method: http.MethodGet, header: http.Header{"Range": {"bytes=999999-"}}, handler: serveMedia, wantStatus: http.StatusRequestedRangeNotSatisfiable},
		{name: "not modified", mark: true, method: http.MethodGet, header: http.Header{"If-Modified-Since": {time.Unix(1_800_000_000, 0).UTC().Format(http.TimeFormat)}}, handler: serveMedia, wantStatus: http.StatusNotModified},
		{name: "failed relay", mark: true, method: http.MethodGet, handler: func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}, wantStatus: http.StatusNotFound},
		{name: "head", mark: true, method: http.MethodHead, handler: serveMedia, wantStatus: http.StatusOK},
		// net/http reports a HEAD body write as written while discarding it, so a
		// handler that streams on HEAD must still record nothing.
		{name: "head whose handler writes a body", mark: true, method: http.MethodHead, handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "remux-bytes")
		}, wantStatus: http.StatusOK},
		{name: "relay that sent no bytes", mark: true, method: http.MethodGet, handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}, wantStatus: http.StatusOK},
		{name: "response not marked as playback", method: http.MethodGet, handler: serveMedia, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, recorded := deliveryCounter(t, tt.mark, tt.handler)
			if got := requestDelivery(t, server, tt.method, tt.header); got != tt.wantStatus {
				t.Fatalf("status = %d, want %d", got, tt.wantStatus)
			}
			if got := recorded.Load() > 0; got != tt.wantRecord {
				t.Fatalf("recorded delivery = %v (%d writes), want %v", got, recorded.Load(), tt.wantRecord)
			}
		})
	}
}

// A progressive response can stream for the length of a film. It must keep
// recording while bytes flow, not only when it starts or ends, and it must do
// so on the zero-copy path that serves files.
func TestDeliveryKeepsRecordingWhileALongResponseFlows(t *testing.T) {
	const size = 4 * meterChunk
	path := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(path, bytes.Repeat([]byte("m"), int(size)), 0o600); err != nil {
		t.Fatal(err)
	}
	server, recorded := deliveryCounter(t, true, func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(path)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = f.Close() }()
		http.ServeContent(w, r, "movie.mp4", time.Unix(1_700_000_000, 0), f)
	})
	if got := requestDelivery(t, server, http.MethodGet, nil); got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if got, want := recorded.Load(), size/meterChunk; got < want {
		t.Fatalf("recorded delivery %d times over %d bytes, want at least %d (one per %d-byte slice)", got, size, want, meterChunk)
	}
}

// Handlers also run without meterEgress (tests call them directly), so a
// missing recorder must be harmless.
func TestDeliveryWithoutARecorderIsANoOp(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	noteDelivery(r, &streamtoken.Claims{SessionID: "session-1"})
	noteDelivery(r, nil)
	w := &meteredResponseWriter{ResponseWriter: httptest.NewRecorder(), meter: newEgressMeter()}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("media")); err != nil {
		t.Fatal(err)
	}
}

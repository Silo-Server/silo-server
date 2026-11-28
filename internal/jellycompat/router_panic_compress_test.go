package jellycompat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// A Jellyfin-compatible handler that panics after writing part of a JSON
// body the compressor holds is answered with the recoverer's 500, not with
// the partial body as a finished response (#5240). The recoverer sits inside
// the compressor, so this drives the router NewRouter builds.
func TestCompatPanicDropsHeldCompressedResponse(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{
		Config:       cfg,
		SessionStore: NewSessionStore(time.Hour, nil),
		IDCodec:      NewResourceIDCodec(),
		SessionMgr:   &testCompatSessionManager{},
	})
	partial := strings.Repeat(`{"Item":1},`, 30)
	router.Get("/silopanicheldtest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(2*len(partial)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, partial)
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/silopanicheldtest", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the recoverer's 500", rec.Code)
	}
	if coding, length := rec.Header().Get("Content-Encoding"), rec.Header().Get("Content-Length"); coding != "" || length != "" {
		t.Fatalf("500 carries the held response's Content-Encoding %q and Content-Length %q", coding, length)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("%d bytes of the held body were sent", rec.Body.Len())
	}
}

// One already streaming when the handler panics can't be replaced by the
// 500, so its connection is broken (#5240).
func TestCompatPanicBreaksStreamedCompressedResponse(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{
		Config:       cfg,
		SessionStore: NewSessionStore(time.Hour, nil),
		IDCodec:      NewResourceIDCodec(),
		SessionMgr:   &testCompatSessionManager{},
	})
	router.Get("/silopanicstreamedtest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat(`{"Item":1},`, 30))
		_ = http.NewResponseController(w).Flush()
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/silopanicstreamedtest", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	var aborted any
	func() {
		defer func() { aborted = recover() }()
		router.ServeHTTP(httptest.NewRecorder(), req)
	}()
	if aborted != http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
		t.Fatalf("panic = %v, want http.ErrAbortHandler to break the connection", aborted)
	}
}

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

// A native handler that panics after writing part of a compressible body
// the compressor holds is answered with the recoverer's 500, not with the
// partial body as a finished response (#5240). The recoverer sits inside the
// compressor, so this drives the real chain.
func TestNativePanicDropsHeldCompressedResponse(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	root := chi.NewRouter()
	useBaseMiddleware(root, Dependencies{Config: cfg})
	partial := strings.Repeat(`{"item":1},`, 30)
	root.Get("/api/v1/panic-held-test", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(2*len(partial)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, partial)
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/panic-held-test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)

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

package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Transcode nodes call the node control routes every second; a request record
// per report would bury the log, while ordinary API requests keep theirs.
func TestRequestLoggerSkipsNodeControlRoutes(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	handler := RequestLogger("test-node")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/api/internal/nodes/self/report", "/api/internal/nodes/self/logs", "/api/internal/nodes/self/sources/open", "/api/internal/nodes/sources"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
	}
	if buf.Len() != 0 {
		t.Fatalf("node control requests were logged:\n%s", buf.String())
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/catalog/items/a", nil))
	if !bytes.Contains(buf.Bytes(), []byte("/api/v1/catalog/items/a")) {
		t.Fatalf("an API request was not logged:\n%s", buf.String())
	}
}

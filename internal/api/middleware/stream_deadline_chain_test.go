package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// TestStreamingDeadlineSurvivesMiddlewareChain guards the Unwrap chain: the
// streaming handlers' rolling write deadline only works if every
// ResponseWriter wrapper between the server and the handler implements
// Unwrap, so http.ResponseController can reach the connection. This assembles
// the real wrapping middlewares from the main API chain (RequestLogger,
// Metrics, Compress) around a streaming handler and proves a slow response
// outlives the server's WriteTimeout. If a new middleware wraps the writer
// without Unwrap, this fails with a truncated body.
func TestStreamingDeadlineSurvivesMiddlewareChain(t *testing.T) {
	const (
		writeEvery = 50 * time.Millisecond
		writes     = 60 // ~3s total, 3x the server WriteTimeout
		chunk      = "0123456789abcdef"
	)

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := httpstream.NewRollingDeadlineWriter(w)
		sw.WriteHeader(http.StatusOK)
		for i := 0; i < writes; i++ {
			if _, err := sw.Write([]byte(chunk)); err != nil {
				return
			}
			sw.Flush()
			time.Sleep(writeEvery)
		}
	})
	// Same order as internal/api/router.go: RequestLogger, Metrics, Compress.
	handler = chimw.Compress(5)(handler)
	handler = apimw.Metrics(handler)
	handler = apimw.RequestLogger("test-node")(handler)

	srv := httptest.NewUnstartedServer(handler)
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
		t.Fatalf("stream died through middleware chain at %d bytes: %v (a wrapper without Unwrap?)", len(body), err)
	}
	if want := writes * len(chunk); len(body) != want {
		t.Fatalf("short body through middleware chain: got %d bytes, want %d", len(body), want)
	}
}

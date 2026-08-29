package api

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// TestMountedNativeRouterServesMediaOverHTTP2 is a does-not-regress check.
// HTTP/2 has no sendfile: the h2 layer frames every response body itself, so
// the io.ReaderFrom fast path the writer chain now preserves is simply unused.
// The wrappers must still behave — correct status, ranges, and an unwrapped
// (uncompressed) media body on a bypassed route — rather than assuming the
// HTTP/1.1 shape of the connection.
func TestMountedNativeRouterServesMediaOverHTTP2(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	telemetryConfig := streamtelemetry.DefaultConfig("http2-test")
	telemetryConfig.Enabled = true
	routes := &socketRoutes{telemetry: streamtelemetry.NewRegistry(telemetryConfig, streamtelemetry.NewLocalStore(), nil)}
	root := chi.NewRouter()
	useBaseMiddleware(root, Dependencies{
		Config:            cfg,
		ActivityLogWriter: socketActivityWriter{},
	})
	routes.Mount(root)

	server := httptest.NewUnstartedServer(root)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := server.Client()
	if tr, ok := client.Transport.(*http.Transport); ok {
		tr.DisableCompression = true
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // httptest self-signed cert
	}
	t.Cleanup(client.CloseIdleConnections)

	mediaURL := server.URL + "/api/v1/stream/socket-test"

	resp, err := client.Get(mediaURL)
	if err != nil {
		t.Fatalf("GET over HTTP/2: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("negotiated HTTP/%d.%d, want HTTP/2 — the test is not exercising h2",
			resp.ProtoMajor, resp.ProtoMinor)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "0123456789abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("HTTP/2 GET = %d %q", resp.StatusCode, body)
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Fatalf("HTTP/2 media Content-Encoding = %q, want empty on a bypassed route", enc)
	}

	req, err := http.NewRequest(http.MethodGet, mediaURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Range", "bytes=2-5")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("ranged GET over HTTP/2: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "2345" {
		t.Fatalf("HTTP/2 Range = %d %q, want 206 %q", resp.StatusCode, body, "2345")
	}
	if snapshot := routes.telemetry.Sweep(); len(snapshot.Sessions) != 1 || snapshot.Sessions[0].RequestCount != 2 {
		t.Fatalf("HTTP/2 telemetry snapshot = %+v", snapshot)
	}
}

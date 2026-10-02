package abs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

type hijackingSocketIOServer struct{}

func (hijackingSocketIOServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unavailable", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
	})
}

func TestMountedStandaloneRouterPreservesSocketIOHijack(t *testing.T) {
	t.Run("telemetry disabled", func(t *testing.T) {
		assertSocketIOHijackSurvives(t, nil)
	})
	// §4.4: "ABS mounts one access-log wrapper across both media and socket.io,
	// so middleware placement decides whether websockets survive." The regression
	// only means anything with the telemetry wrapper actually mounted — observeABS
	// wraps per route precisely so nothing lands between engine.io and the raw
	// connection.
	t.Run("telemetry enabled", func(t *testing.T) {
		assertSocketIOHijackSurvives(t, telemetryRegistry(t))
	})
}

func assertSocketIOHijackSurvives(t *testing.T, registry *streamtelemetry.Registry) {
	t.Helper()
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Use(httpstream.CompressExcept(5, SkipMediaCompression))
	handler := New(Dependencies{MediaStore: noopMediaStore{}, SocketIO: hijackingSocketIOServer{}})
	handler.SetStreamTelemetry(registry)
	handler.Mount(router)

	server := httptest.NewUnstartedServer(router)
	server.Start()
	t.Cleanup(server.Close)

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	req, err := http.NewRequest(http.MethodGet, server.URL+"/socket.io/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("socket.io upgrade: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d, want 101", resp.StatusCode)
	}
}

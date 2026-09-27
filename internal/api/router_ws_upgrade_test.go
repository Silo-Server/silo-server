package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/config"
)

// The WebSocket routes (/events/ws, playback control, watch-together, admin
// logs) upgrade through gorilla, which asserts http.Hijacker on the writer
// the middleware chain hands it. The chain built by useBaseMiddleware wraps
// every response in the compression exclusion writer, so an upgrade must
// still succeed through it over a real socket.
func TestMountedRouterUpgradesWebSocketsThroughBaseMiddleware(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	root := chi.NewRouter()
	useBaseMiddleware(root, Dependencies{Config: cfg})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	root.Get("/api/v1/events/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("hello"))
	})
	server := httptest.NewServer(root)
	t.Cleanup(server.Close)

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/events/ws", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("Dial: %v (status %d)", err, status)
	}
	defer func() { _ = conn.Close() }()
	_, msg, err := conn.ReadMessage()
	if err != nil || string(msg) != "hello" {
		t.Fatalf("ReadMessage: %q %v", msg, err)
	}
}

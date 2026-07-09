package jellycompat

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsMessage struct {
	MessageType string          `json:"MessageType"`
	Data        json.RawMessage `json:"Data,omitempty"`
}

// HandleSocket upgrades the connection to a WebSocket and keeps it alive.
// Jellyfin clients (e.g. Streamyfin) open a persistent WebSocket for
// remote-control commands. This stub accepts the connection and responds
// to KeepAlive pings but sends no server-initiated commands.
func HandleSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "websocket upgrade failed", "component", "jellycompat", "error", err)
		return
	}
	defer conn.Close()

	slog.InfoContext(r.Context(), "websocket connected", "component", "jellycompat",
		"remote_addr", r.RemoteAddr,
		"device_id", r.URL.Query().Get("deviceId"),
	)

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.WarnContext(r.Context(), "websocket read error", "component", "jellycompat", "error", err)
			}
			return
		}

		var msg wsMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.MessageType {
		case "KeepAlive":
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		default:
			slog.DebugContext(r.Context(), "websocket message ignored", "component", "jellycompat", "type", msg.MessageType)
		}
	}
}

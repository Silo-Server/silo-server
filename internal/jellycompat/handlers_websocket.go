package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const wsKeepAlive = "KeepAlive"

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

type wsMessage struct {
	MessageType string          `json:"MessageType"`
	Data        json.RawMessage `json:"Data,omitempty"`
}

// NewSocketHandler implements Jellyfin's application KeepAlive protocol without
// advertising remote-control commands. Tokens are revalidated while connected.
func NewSocketHandler(sessions *SessionStore, keys *AdminAPIKeyAuthenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := ExtractToken(r)
		validate := func() bool {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if strings.HasPrefix(token, "sa_") {
				session, _, _ := keys.resolveSession(ctx, token)
				return session != nil
			}
			if sessions == nil {
				return false
			}
			if sessions.repo != nil {
				session, err := sessions.repo.GetByToken(ctx, token, sessions.now())
				return err == nil && session != nil
			}
			_, ok := sessions.Get(token)
			return ok
		}
		if !ok || !validate() {
			writeError(w, 401, "Unauthorized", "Invalid or expired authentication token")
			return
		}
		serveCompatSocket(w, r, validate, 12*time.Second)
	}
}

func serveCompatSocket(w http.ResponseWriter, r *http.Request, validate func() bool, checkInterval time.Duration) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(64 * 1024)
	done := make(chan struct{})
	defer close(done)
	messages := make(chan wsMessage, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			var msg wsMessage
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			select {
			case messages <- msg:
			case <-done:
				return
			}
		}
	}()
	write := func(message wsMessage) error {
		if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		return conn.WriteJSON(message)
	}
	force := wsMessage{MessageType: "ForceKeepAlive", Data: json.RawMessage("60")}
	if err := write(force); err != nil {
		return
	}
	lastKeepAlive := time.Now()
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-readDone:
			return
		case msg := <-messages:
			if msg.MessageType == wsKeepAlive {
				lastKeepAlive = time.Now()
				if err := write(wsMessage{MessageType: wsKeepAlive}); err != nil {
					return
				}
			}
		case <-ticker.C:
			if !validate() {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "Authentication expired"), time.Now().Add(time.Second))
				return
			}
			elapsed := time.Since(lastKeepAlive)
			if elapsed >= 60*time.Second {
				return
			}
			if elapsed >= 45*time.Second {
				if err := write(force); err != nil {
					return
				}
			}
		}
	}
}

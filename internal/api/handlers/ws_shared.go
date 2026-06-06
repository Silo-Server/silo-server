package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsPingInterval = 20 * time.Second
	wsPongTimeout  = 10 * time.Second
	wsWriteTimeout = 5 * time.Second
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: checkWebSocketOrigin,
}

func checkWebSocketOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil || originURL.Host == "" {
		return false
	}

	if strings.EqualFold(originURL.Host, r.Host) {
		return true
	}

	// Behind a TLS-terminating CDN/proxy, r.Host is the internal origin host
	// while the public host the browser used arrives as X-Forwarded-Host. The
	// browser's Origin reflects that public host, so accept it too.
	if fwd := forwardedHost(r); fwd != "" && strings.EqualFold(originURL.Host, fwd) {
		return true
	}

	return false
}

func configureWebSocket(conn *websocket.Conn) {
	if conn == nil {
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	})
}

func startWebSocketPingLoop(ctx context.Context, writePing func() error) {
	if writePing == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := writePing(); err != nil {
					return
				}
			}
		}
	}()
}

func writeWebSocketError(conn *websocket.Conn, code, message string) {
	if conn == nil {
		return
	}
	_ = conn.WriteJSON(map[string]string{
		"type":    "error",
		"code":    code,
		"message": message,
	})
}

func writeWebSocketJSON(conn *websocket.Conn, value any) error {
	if conn == nil {
		return websocket.ErrCloseSent
	}
	if err := conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout)); err != nil {
		return err
	}
	defer conn.SetWriteDeadline(time.Time{})
	return conn.WriteJSON(value)
}

func writeWebSocketControl(conn *websocket.Conn, messageType int, data []byte) error {
	if conn == nil {
		return websocket.ErrCloseSent
	}
	return conn.WriteControl(messageType, data, time.Now().Add(wsWriteTimeout))
}

func readWebSocketJSON[T any](data []byte, target *T) error {
	return json.Unmarshal(data, target)
}

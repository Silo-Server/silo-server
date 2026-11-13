package handlers

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsPingInterval = 20 * time.Second
	wsPongTimeout  = 10 * time.Second
	wsWriteTimeout = 5 * time.Second

	// wsMaxClientMessageBytes bounds one inbound message on every socket that
	// configureWebSocket sets up. Clients send only small JSON control frames
	// (playback hello/ack/result, watch-together reports, events subscribe),
	// each a few hundred bytes; the limit stops a client from making the
	// server buffer an arbitrarily large message before it is parsed. Gorilla
	// counts every fragment of a message, and an oversize message fails the
	// read and closes the connection with 1009 (message too big).
	wsMaxClientMessageBytes = 64 * 1024
)

// wsOverlayOrigins feeds the shared v1 upgrader the overlay origins connected
// network access providers report. The upgrader is package state, so its
// source is too; SetWebSocketOverlayOrigins is called once at router build.
var wsOverlayOrigins atomic.Pointer[OverlayOriginSource]

// SetWebSocketOverlayOrigins installs the overlay origin source consulted by
// the shared v1 WebSocket upgrader.
func SetWebSocketOverlayOrigins(source OverlayOriginSource) {
	wsOverlayOrigins.Store(&source)
}

func configureWebSocket(conn *websocket.Conn) {
	if conn == nil {
		return
	}

	conn.SetReadLimit(wsMaxClientMessageBytes)
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

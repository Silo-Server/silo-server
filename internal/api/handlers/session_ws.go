package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type realtimeClientMessage struct {
	Type playback.RealtimeMessageType `json:"type"`
}

type sessionRealtimeConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func (c *sessionRealtimeConn) WriteJSON(v any) error {
	if c == nil || c.conn == nil {
		return playback.ErrRealtimeConnectionNotFound
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeWebSocketJSON(c.conn, v)
}

func (c *sessionRealtimeConn) WritePing() error {
	if c == nil || c.conn == nil {
		return playback.ErrRealtimeConnectionNotFound
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeWebSocketControl(c.conn, websocket.PingMessage, nil)
}

// HandleSessionWebSocket handles GET /playback/ws/{session_id}.
// It upgrades to a realtime control WebSocket. Sessions become control-ready
// only after a validated hello message. Disconnects degrade command delivery
// but do not stop an otherwise valid playback session.
func (h *PlaybackHandler) HandleSessionWebSocket(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.RealtimeHub == nil {
		http.Error(w, "realtime unavailable", http.StatusServiceUnavailable)
		return
	}

	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	setPlaybackSessionLogContext(r, sessionID)

	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		writePlaybackSessionNotFound(w)
		return
	}
	if session.UserID != userID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "websocket upgrade failed", "component", "api", "error", err, "session", sessionID, "playback_session_id", sessionID)
		return
	}

	realtimeConn := &sessionRealtimeConn{conn: conn}
	registration := h.RealtimeHub.Register(sessionID, realtimeConn)
	if registration == nil {
		conn.Close()
		slog.WarnContext(r.Context(), "failed to register realtime websocket", "component", "api", "session", sessionID, "playback_session_id", sessionID)
		return
	}

	defer func() {
		if h.setRealtimeConnectionState(sessionID, false) {
			h.syncSessionsNow(context.Background(), "realtime_disconnect")
		}
		h.RealtimeHub.Unregister(registration)
		_ = conn.Close()
	}()

	configureWebSocket(conn)
	ctx, cancelRead := context.WithCancel(r.Context())
	defer cancelRead()
	startWebSocketPingLoop(ctx, realtimeConn.WritePing)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if err := h.handleRealtimeClientMessage(sessionID, data); err != nil {
			slog.WarnContext(r.Context(), "invalid realtime client message", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
	}
}

func (h *PlaybackHandler) handleRealtimeClientMessage(sessionID string, data []byte) error {
	var base realtimeClientMessage
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}

	switch base.Type {
	case playback.RealtimeMessageTypeHello:
		var hello playback.HelloEnvelope
		if err := json.Unmarshal(data, &hello); err != nil {
			return err
		}
		if err := hello.Validate(); err != nil {
			return err
		}
		if hello.SessionID != sessionID {
			return playback.ErrInvalidRealtimePayload
		}
		if h.setRealtimeConnectionState(sessionID, true) {
			h.syncSessionsNow(context.Background(), "realtime_hello")
		}
		h.touchSessionActivity(sessionID)
		return nil
	case playback.RealtimeMessageTypeAck:
		var ack playback.AckEnvelope
		if err := json.Unmarshal(data, &ack); err != nil {
			return err
		}
		if err := ack.Validate(); err != nil {
			return err
		}
		if ack.SessionID != sessionID {
			return playback.ErrInvalidRealtimePayload
		}
		h.touchSessionActivity(sessionID)
		if h.CommandTracker != nil {
			h.CommandTracker.Ack(ack.CommandID)
		}
		return nil
	case playback.RealtimeMessageTypeResult:
		var result playback.ResultEnvelope
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if err := result.Validate(); err != nil {
			return err
		}
		if result.SessionID != sessionID {
			return playback.ErrInvalidRealtimePayload
		}
		h.touchSessionActivity(sessionID)
		if h.CommandTracker != nil {
			h.CommandTracker.Result(result.CommandID)
		}
		record, ok := h.getRealtimeCommand(result.CommandID)
		if !ok {
			return nil
		}
		h.forgetRealtimeCommand(result.CommandID)
		if record.SessionID != sessionID {
			return playback.ErrInvalidRealtimePayload
		}
		if result.Status != playback.RealtimeResultStatusCompleted {
			return nil
		}
		switch record.Name {
		case playback.CommandStop, playback.CommandTerminate:
			err := h.stopPlaybackSessionByID(context.Background(), sessionID, true)
			if err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
				slog.Error("failed to stop playback after realtime completion", "session", sessionID, "playback_session_id", sessionID, "error", err)
			}
		}
		return nil
	default:
		return playback.ErrInvalidRealtimePayload
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

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

type sessionRealtimeClient struct {
	handler       *PlaybackHandler
	connectionCtx context.Context
	registration  *playback.RealtimeRegistration
	sessionID     string
	helloReceived bool
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
	registration := h.registerSessionRealtimeConnection(sessionID, realtimeConn)
	if registration == nil {
		conn.Close()
		slog.WarnContext(r.Context(), "failed to register realtime websocket", "component", "api", "session", sessionID, "playback_session_id", sessionID)
		return
	}

	configureWebSocket(conn)
	ctx, cancelRead := context.WithCancel(r.Context())
	defer func() {
		cancelRead()
		h.unregisterSessionRealtimeConnection(sessionID, registration)
		_ = conn.Close()
	}()

	startWebSocketPingLoop(ctx, realtimeConn.WritePing)
	client := &sessionRealtimeClient{
		handler:       h,
		connectionCtx: ctx,
		registration:  registration,
		sessionID:     sessionID,
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if err := client.handleMessage(data); err != nil {
			slog.WarnContext(r.Context(), "invalid realtime client message", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
	}
}

func (c *sessionRealtimeClient) handleMessage(data []byte) error {
	if c == nil || c.handler == nil || c.sessionID == "" {
		return playback.ErrInvalidRealtimePayload
	}
	h := c.handler
	sessionID := c.sessionID
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
		firstHello := !c.helloReceived
		c.helloReceived = true
		h.markSessionRealtimeReady(sessionID, c.registration)
		h.touchSessionActivity(sessionID)
		if firstHello {
			go h.sendCurrentMarkerSnapshot(c.connectionCtx, c.registration, sessionID)
		}
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
		// Establish ownership before mutating anything: a result naming another
		// session's command must be rejected without canceling that command's
		// deadline or dropping its record. An unknown command_id is not an
		// error — a duplicate or late result for an already-completed command
		// is normal traffic.
		record, ok := h.getRealtimeCommand(result.CommandID)
		if ok && record.SessionID != sessionID {
			return playback.ErrInvalidRealtimePayload
		}
		if h.CommandTracker != nil {
			h.CommandTracker.Result(result.CommandID)
		}
		if !ok {
			return nil
		}
		h.forgetRealtimeCommand(result.CommandID)
		if result.Status != playback.RealtimeResultStatusCompleted {
			// A rejected plan_invalidated leaves the client running a route the
			// server has withdrawn, and the tracker's deadline was already
			// canceled by the result. Fall back to the same session stop an
			// unnegotiated client gets; its recovery replans against the
			// persisted verdict.
			if record.Name == playback.CommandPlanInvalidated {
				slog.Warn("client rejected a plan invalidation; stopping the session",
					"session", sessionID, "playback_session_id", sessionID, "error", result.Error)
				if err := h.stopPlaybackSessionByID(context.Background(), sessionID, false); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
					slog.Error("failed to stop playback after a rejected plan invalidation", "session", sessionID, "playback_session_id", sessionID, "error", err)
				}
			}
			return nil
		}
		switch record.Name {
		case playback.CommandStop, playback.CommandTerminate:
			err := h.stopPlaybackSessionByID(context.Background(), sessionID, true)
			if err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
				slog.Error("failed to stop playback after realtime completion", "session", sessionID, "playback_session_id", sessionID, "error", err)
			}
		case playback.CommandPlanInvalidated:
			// Completion means the client replanned itself; the session stays
			// alive on its replacement plan and nothing else is required here.
		}
		return nil
	default:
		return playback.ErrInvalidRealtimePayload
	}
}

func (h *PlaybackHandler) registerSessionRealtimeConnection(
	sessionID string,
	conn playback.RealtimeConnection,
) *playback.RealtimeRegistration {
	h.realtimeConnectionMu.Lock()
	defer h.realtimeConnectionMu.Unlock()
	registration := h.RealtimeHub.Register(sessionID, conn)
	if registration != nil && h.setRealtimeConnectionState(sessionID, false) {
		h.syncSessionsNow(context.Background(), "realtime_pending_hello")
	}
	return registration
}

func (h *PlaybackHandler) markSessionRealtimeReady(
	sessionID string,
	registration *playback.RealtimeRegistration,
) {
	h.realtimeConnectionMu.Lock()
	defer h.realtimeConnectionMu.Unlock()
	if !h.RealtimeHub.HasRegistration(registration) {
		return
	}
	if h.setRealtimeConnectionState(sessionID, true) {
		h.syncSessionsNow(context.Background(), "realtime_hello")
	}
}

func (h *PlaybackHandler) unregisterSessionRealtimeConnection(
	sessionID string,
	registration *playback.RealtimeRegistration,
) {
	h.realtimeConnectionMu.Lock()
	defer h.realtimeConnectionMu.Unlock()
	if !h.RealtimeHub.Unregister(registration) || h.RealtimeHub.HasConnection(sessionID) {
		return
	}
	if h.setRealtimeConnectionState(sessionID, false) {
		h.syncSessionsNow(context.Background(), "realtime_disconnect")
	}
}

type playbackMarkerSnapshotNotifier interface {
	SendSessionSnapshotFromLoader(
		ctx context.Context,
		registration *playback.RealtimeRegistration,
		fileID int,
		loader playback.MarkerSnapshotFileLoader,
	) (bool, error)
}

func (h *PlaybackHandler) sendCurrentMarkerSnapshot(
	connectionCtx context.Context,
	registration *playback.RealtimeRegistration,
	sessionID string,
) {
	if h == nil || h.fileResolver == nil || h.MarkerUpdateNotifier == nil {
		return
	}
	notifier, ok := h.MarkerUpdateNotifier.(playbackMarkerSnapshotNotifier)
	if !ok {
		slog.DebugContext(connectionCtx, "playback marker snapshot unavailable", "component", "api",
			"session", sessionID, "playback_session_id", sessionID,
			"reason", "notifier does not support persisted snapshots")
		return
	}
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil || session == nil || session.MediaFileID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(connectionCtx, 3*time.Second)
	defer cancel()
	sent, err := notifier.SendSessionSnapshotFromLoader(ctx, registration, session.MediaFileID, h.fileResolver)
	if err != nil {
		slog.DebugContext(ctx, "playback marker snapshot unavailable", "component", "api",
			"session", sessionID, "playback_session_id", sessionID,
			"file_id", session.MediaFileID, "error", err)
		return
	}
	if !sent {
		slog.DebugContext(ctx, "playback marker snapshot skipped", "component", "api",
			"session", sessionID, "playback_session_id", sessionID,
			"file_id", session.MediaFileID)
	}
}

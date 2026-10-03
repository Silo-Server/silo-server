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
	"github.com/Silo-Server/silo-server/internal/models"
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

	snapshotStarted := false
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if err := h.handleRealtimeClientMessage(sessionID, data); err != nil {
			slog.WarnContext(r.Context(), "invalid realtime client message", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
			continue
		}
		h.afterRealtimeClientMessage(ctx, registration, sessionID, data, &snapshotStarted)
	}
}

// afterRealtimeClientMessage runs after a client message was handled without
// error, on both the legacy and the v2 control socket. Marker updates reach a
// session only while it has a ready realtime connection, so a player that
// reconnects may have missed one. After the connection's first valid hello, it
// is sent the stored markers.
func (h *PlaybackHandler) afterRealtimeClientMessage(ctx context.Context, registration *playback.RealtimeRegistration, sessionID string, data []byte, snapshotStarted *bool) {
	if *snapshotStarted || !isRealtimeHello(data) {
		return
	}
	*snapshotStarted = true
	go h.sendRealtimeMarkerSnapshot(ctx, registration, sessionID)
}

// playbackMarkerSnapshotSender is implemented by the marker notifier that can
// deliver stored markers to a single realtime registration.
type playbackMarkerSnapshotSender interface {
	SendSnapshot(ctx context.Context, registration *playback.RealtimeRegistration, fileID int, load func(context.Context, int) (*models.MediaFile, error)) (bool, error)
}

func isRealtimeHello(data []byte) bool {
	var base realtimeClientMessage
	return json.Unmarshal(data, &base) == nil && base.Type == playback.RealtimeMessageTypeHello
}

// sendRealtimeMarkerSnapshot runs off the read loop: it reads the database and
// writes to the socket, and holds no lock shared with other sessions. In
// on-demand storage, online markers are never saved, so the stored row would
// be missing markers the player already shows and the snapshot would clear
// them; the snapshot is skipped there.
func (h *PlaybackHandler) sendRealtimeMarkerSnapshot(ctx context.Context, registration *playback.RealtimeRegistration, sessionID string) {
	sender, ok := h.MarkerUpdateNotifier.(playbackMarkerSnapshotSender)
	if !ok || h.fileResolver == nil {
		return
	}
	if onlineMarkersOnDemand(ctx, h.SettingsRepo) {
		return
	}
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil || session == nil || session.MediaFileID <= 0 {
		return
	}
	if _, err := sender.SendSnapshot(ctx, registration, session.MediaFileID, h.fileResolver.GetByID); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "failed to send realtime marker snapshot", "component", "api", "session", sessionID, "playback_session_id", sessionID, "file_id", session.MediaFileID, "error", err)
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

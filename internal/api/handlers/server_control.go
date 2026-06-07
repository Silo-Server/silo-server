package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

var ErrServerRestartAlreadyRequested = errors.New("server restart already requested")

type ServerControlHandler struct {
	requestRestart func(ctx context.Context) error
	commands       *playback.CommandDispatcher
}

type serverRestartRequest struct {
	Reason  string `json:"reason"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

type serverRestartResponse struct {
	Status           string `json:"status"`
	Message          string `json:"message"`
	NotifiedSessions int    `json:"notified_sessions"`
}

func NewServerControlHandler(
	requestRestart func(ctx context.Context) error,
	commands *playback.CommandDispatcher,
) *ServerControlHandler {
	return &ServerControlHandler{
		requestRestart: requestRestart,
		commands:       commands,
	}
}

// HandleRestart handles POST /admin/server/restart.
func (h *ServerControlHandler) HandleRestart(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.requestRestart == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Server restart is unavailable")
		return
	}

	var req serverRestartRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	notifiedSessions := h.notifyPlaybackSessions(req)

	if err := h.requestRestart(context.Background()); err != nil {
		if errors.Is(err, ErrServerRestartAlreadyRequested) {
			writeJSON(w, http.StatusAccepted, serverRestartResponse{
				Status:           "already_requested",
				Message:          "Server restart is already in progress.",
				NotifiedSessions: notifiedSessions,
			})
			return
		}
		slog.Error("server restart request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to request server restart")
		return
	}

	writeJSON(w, http.StatusAccepted, serverRestartResponse{
		Status:           "restart_requested",
		Message:          "Server restart requested. The process will shut down gracefully.",
		NotifiedSessions: notifiedSessions,
	})
}

func (h *ServerControlHandler) notifyPlaybackSessions(req serverRestartRequest) int {
	if h == nil || h.commands == nil {
		return 0
	}

	title := req.Title
	if title == "" {
		title = "Server restarting"
	}
	message := req.Message
	if message == "" {
		message = "The server is restarting now. Playback may reconnect shortly."
	}

	payload, err := json.Marshal(map[string]string{
		"title":   title,
		"message": message,
	})
	if err != nil {
		slog.Warn("server restart: failed to build realtime payload", "error", err)
		return 0
	}

	results := h.commands.DispatchToAll(func(session *playback.Session) (playback.CommandEnvelope, time.Duration, func(), error) {
		if session == nil {
			return playback.CommandEnvelope{}, 0, nil, playback.ErrCommandDispatchUnavailable
		}
		command, err := playback.NewCommandEnvelope(
			session.ID,
			uuid.NewString(),
			playback.CommandServerRestarting,
			payload,
		)
		if err != nil {
			return playback.CommandEnvelope{}, 0, nil, err
		}
		command.Reason = req.Reason
		if command.Reason == "" {
			command.Reason = "server_restart_requested"
		}
		command.IssuedBy = &playback.CommandIssuedBy{Kind: "admin"}
		return command, 0, nil, nil
	})

	delivered := 0
	for _, result := range results {
		if result.Delivered {
			delivered++
			continue
		}
		if result.DispatchErr != nil && !errors.Is(result.DispatchErr, playback.ErrRealtimeConnectionNotFound) {
			slog.Warn("server restart: failed to notify playback session",
				"session_id", result.SessionID,
				"error", result.DispatchErr,
			)
		}
	}
	return delivered
}

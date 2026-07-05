package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	defaultPlaybackControlDeadline = 3 * time.Second
	maxPlaybackControlDeadline     = 10 * time.Second
)

type AdminPlaybackControlHandler struct {
	playback *PlaybackHandler
}

type playbackControlRequest struct {
	Reason     string `json:"reason"`
	Title      string `json:"title"`
	Message    string `json:"message"`
	DeadlineMS int    `json:"deadline_ms"`
}

type playbackControlResponse struct {
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
}

func requiresLivePlaybackControl(name playback.CommandName) bool {
	switch name {
	case playback.CommandPause, playback.CommandUnpause:
		return true
	default:
		return false
	}
}

func NewAdminPlaybackControlHandler(playbackHandler *PlaybackHandler) *AdminPlaybackControlHandler {
	return &AdminPlaybackControlHandler{playback: playbackHandler}
}

func (h *AdminPlaybackControlHandler) HandleStopSession(w http.ResponseWriter, r *http.Request) {
	h.handleSessionCommand(w, r, playback.CommandStop)
}

func (h *AdminPlaybackControlHandler) HandlePauseSession(w http.ResponseWriter, r *http.Request) {
	h.handleSessionCommand(w, r, playback.CommandPause)
}

func (h *AdminPlaybackControlHandler) HandleResumeSession(w http.ResponseWriter, r *http.Request) {
	h.handleSessionCommand(w, r, playback.CommandUnpause)
}

func (h *AdminPlaybackControlHandler) HandleTerminateSession(w http.ResponseWriter, r *http.Request) {
	h.handleSessionCommand(w, r, playback.CommandTerminate)
}

func (h *AdminPlaybackControlHandler) HandleMessageSession(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.playback == nil || h.playback.CommandDispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Playback control is unavailable")
		return
	}

	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Session ID is required")
		return
	}

	if _, err := h.playback.sessionMgr.GetSession(sessionID); err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Playback session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session")
		return
	}

	var req playbackControlRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Message is required")
		return
	}

	payload, err := json.Marshal(map[string]string{
		"title":   req.Title,
		"message": req.Message,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build command payload")
		return
	}

	commandID := uuid.NewString()
	command, err := playback.NewCommandEnvelope(sessionID, commandID, playback.CommandDisplayMessage, payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build command")
		return
	}
	command.Reason = req.Reason
	command.IssuedBy = &playback.CommandIssuedBy{Kind: "admin"}

	result := h.playback.CommandDispatcher.DispatchToSession(command, 0, nil)
	if result.DispatchErr != nil {
		status := http.StatusInternalServerError
		code := "internal_error"
		message := "Failed to dispatch command"
		if errors.Is(result.DispatchErr, playback.ErrRealtimeConnectionNotFound) {
			status = http.StatusConflict
			code = "realtime_unavailable"
			message = "Realtime connection unavailable for playback session"
		}
		writeError(w, status, code, message)
		return
	}

	writeJSON(w, http.StatusAccepted, playbackControlResponse{
		CommandID: commandID,
		Status:    "dispatched",
	})
}

func (h *AdminPlaybackControlHandler) handleSessionCommand(w http.ResponseWriter, r *http.Request, name playback.CommandName) {
	if h == nil || h.playback == nil || h.playback.CommandDispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Playback control is unavailable")
		return
	}

	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Session ID is required")
		return
	}

	session, err := h.playback.sessionMgr.GetSession(sessionID)
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Playback session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session")
		return
	}

	var req playbackControlRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if requiresLivePlaybackControl(name) && (session == nil || !session.HasRealtimeConnection) {
		writeError(w, http.StatusConflict, "realtime_unavailable", "Realtime connection unavailable for playback session")
		return
	}

	commandID := uuid.NewString()
	command, err := playback.NewCommandEnvelope(sessionID, commandID, name, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build command")
		return
	}
	command.Reason = req.Reason
	command.IssuedBy = &playback.CommandIssuedBy{Kind: "admin"}
	deadline := boundedPlaybackControlDeadline(req.DeadlineMS)
	command.DeadlineMS = int(deadline / time.Millisecond)

	fallback := func() {
		h.playback.forgetRealtimeCommand(commandID)
		_ = h.playback.stopPlaybackSessionByID(context.Background(), sessionID, true)
	}

	h.playback.rememberRealtimeCommand(commandID, sessionID, name)
	result := h.playback.CommandDispatcher.DispatchToSession(command, deadline, fallback)
	if result.DispatchErr == nil {
		writeJSON(w, http.StatusAccepted, playbackControlResponse{
			CommandID: commandID,
			Status:    "dispatched",
		})
		return
	}

	h.playback.forgetRealtimeCommand(commandID)
	if errors.Is(result.DispatchErr, playback.ErrRealtimeConnectionNotFound) {
		time.AfterFunc(deadline, fallback)
		writeJSON(w, http.StatusAccepted, playbackControlResponse{
			CommandID: commandID,
			Status:    "fallback_scheduled",
		})
		return
	}

	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to dispatch command")
}

func decodeOptionalJSONBody(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, http.ErrBodyNotAllowed) {
			return nil
		}
		return err
	}
	return nil
}

func boundedPlaybackControlDeadline(deadlineMS int) time.Duration {
	if deadlineMS <= 0 {
		return defaultPlaybackControlDeadline
	}
	deadline := time.Duration(deadlineMS) * time.Millisecond
	if deadline > maxPlaybackControlDeadline {
		return maxPlaybackControlDeadline
	}
	return deadline
}

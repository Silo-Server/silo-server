package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	defaultPlaybackControlDeadline = 3 * time.Second
	maxPlaybackControlDeadline     = 10 * time.Second
	playbackControlBodyLimit       = 16 << 10
	playbackFinalizationTimeout    = 10 * time.Second
)

type AdminPlaybackControlHandler struct {
	playback    *PlaybackHandler
	snapshots   *playback.SnapshotRegistry
	idempotency *playback.IdempotencyStore
	serverID    string
}

type playbackControlRequest struct {
	Reason            string `json:"reason"`
	ReasonCode        string `json:"reason_code,omitempty"`
	Title             string `json:"title"`
	Message           string `json:"message"`
	DeadlineMS        int    `json:"deadline_ms"`
	SessionGeneration string `json:"session_generation,omitempty"`
	SnapshotID        string `json:"snapshot_id,omitempty"`
	IdempotencyKey    string `json:"idempotency_key,omitempty"`

	sessionGenerationPresent bool
	snapshotIDPresent        bool
	idempotencyKeyPresent    bool
}

func (p *playbackControlRequest) UnmarshalJSON(data []byte) error {
	type wire playbackControlRequest
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*p = playbackControlRequest(decoded)
	_, p.sessionGenerationPresent = fields["session_generation"]
	_, p.snapshotIDPresent = fields["snapshot_id"]
	_, p.idempotencyKeyPresent = fields["idempotency_key"]
	return nil
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

// NewGuardedAdminPlaybackControlHandler constructs the compatibility playback
// controls plus generation-bound automated termination dependencies.
func NewGuardedAdminPlaybackControlHandler(
	playbackHandler *PlaybackHandler,
	snapshots *playback.SnapshotRegistry,
	idempotency *playback.IdempotencyStore,
	serverID string,
) *AdminPlaybackControlHandler {
	return &AdminPlaybackControlHandler{
		playback:    playbackHandler,
		snapshots:   snapshots,
		idempotency: idempotency,
		serverID:    strings.TrimSpace(serverID),
	}
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
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, playbackControlBodyLimit)
	}
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Playback control request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if !validPlaybackControlFields(sessionID, req) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "Playback control request contains an oversized field")
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

	var req playbackControlRequest
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, playbackControlBodyLimit)
	}
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Playback control request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if !validPlaybackControlFields(sessionID, req) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "Playback control request contains an oversized field")
		return
	}
	if name == playback.CommandTerminate {
		guardFields := 0
		for _, present := range []bool{req.sessionGenerationPresent, req.snapshotIDPresent, req.idempotencyKeyPresent} {
			if present {
				guardFields++
			}
		}
		if guardFields != 0 {
			if guardFields != 3 || strings.TrimSpace(req.SessionGeneration) == "" || strings.TrimSpace(req.SnapshotID) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
				writeError(w, http.StatusUnprocessableEntity, "invalid_termination_guard", "Session generation, snapshot ID, and idempotency key must be provided together and non-empty")
				return
			}
			h.handleGenerationBoundTermination(w, r, sessionID, req)
			return
		}
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

	if requiresLivePlaybackControl(name) && (session == nil || !session.HasRealtimeConnection) {
		writeError(w, http.StatusConflict, "realtime_unavailable", "Realtime connection unavailable for playback session")
		return
	}
	if name == playback.CommandTerminate {
		h.handleLegacyTermination(w, r, session, req)
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
		if err := h.playback.stopPlaybackSessionByID(context.Background(), sessionID, true); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
			slog.Error("playback control fallback could not durably stop session", "session", sessionID, "command", name, "error", err)
		}
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

type exactSessionTerminator interface {
	TerminateSessionGenerationSnapshotStatus(context.Context, string, string, func(*playback.Session) error) (playback.TerminationStatus, error)
}

func (h *AdminPlaybackControlHandler) handleLegacyTermination(w http.ResponseWriter, r *http.Request, session *playback.Session, req playbackControlRequest) {
	terminator, ok := h.playback.sessionMgr.(exactSessionTerminator)
	if !ok || session == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Playback termination is unavailable")
		return
	}
	commandID := uuid.NewString()
	command, err := playback.NewCommandEnvelope(session.ID, commandID, playback.CommandTerminate, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build command")
		return
	}
	command.Reason = req.Reason
	command.IssuedBy = &playback.CommandIssuedBy{Kind: "admin"}
	deadline := boundedPlaybackControlDeadline(req.DeadlineMS)
	command.DeadlineMS = int(deadline / time.Millisecond)
	delivered := false
	_, err = terminator.TerminateSessionGenerationSnapshotStatus(r.Context(), session.ID, session.Generation, func(snapshot *playback.Session) error {
		if snapshot == nil {
			return nil
		}
		finalizeCtx, cancel := context.WithTimeout(context.Background(), playbackFinalizationTimeout)
		defer cancel()
		if h.playback.RealtimeHub != nil {
			if sendErr := h.playback.RealtimeHub.Send(snapshot.ID, command); sendErr == nil {
				delivered = true
			} else if !errors.Is(sendErr, playback.ErrRealtimeConnectionNotFound) {
				slog.WarnContext(finalizeCtx, "legacy realtime termination delivery failed; completing server-side fallback", "session", snapshot.ID, "error", sendErr)
			}
		}
		h.playback.finalizeSessionStop(finalizeCtx, snapshot, true, "terminate", true)
		return nil
	})
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Playback session not found")
		} else if errors.Is(err, playback.ErrSessionSuperseded) {
			writeError(w, http.StatusConflict, "session_generation_conflict", "Playback session generation changed")
		} else {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to terminate playback session")
		}
		return
	}
	status := "fallback_scheduled"
	if delivered {
		status = "dispatched"
	}
	writeJSON(w, http.StatusAccepted, playbackControlResponse{CommandID: commandID, Status: status})
}

func (h *AdminPlaybackControlHandler) handleGenerationBoundTermination(
	w http.ResponseWriter,
	r *http.Request,
	sessionID string,
	req playbackControlRequest,
) {
	if h.snapshots == nil || h.idempotency == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Generation-bound playback termination is unavailable")
		return
	}
	terminator, ok := h.playback.sessionMgr.(exactSessionTerminator)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Generation-bound playback termination is unavailable")
		return
	}
	generation := strings.TrimSpace(req.SessionGeneration)
	deadlineMS := int(boundedPlaybackControlDeadline(req.DeadlineMS) / time.Millisecond)
	binding := playback.TerminationBinding{
		ServerID:   h.serverID,
		SessionID:  sessionID,
		Generation: generation,
		SnapshotID: strings.TrimSpace(req.SnapshotID),
		ReasonCode: req.ReasonCode,
		Reason:     req.Reason,
		Title:      req.Title,
		Message:    req.Message,
		DeadlineMS: deadlineMS,
	}
	result, _, err := h.idempotency.Do(strings.TrimSpace(req.IdempotencyKey), binding, func() (playback.TerminationResult, error) {
		identity := playback.SnapshotSessionIdentity{SessionID: sessionID, Generation: generation}
		if err := h.snapshots.Validate(strings.TrimSpace(req.SnapshotID), identity); err != nil {
			return playback.TerminationResult{}, err
		}
		session, err := h.playback.sessionMgr.GetSession(sessionID)
		if err != nil {
			return playback.TerminationResult{}, err
		}
		if session == nil {
			return playback.TerminationResult{}, playback.ErrSessionNotFound
		}
		if session.Generation != generation {
			return playback.TerminationResult{}, playback.ErrSessionSuperseded
		}
		commandID := uuid.NewString()
		var payload json.RawMessage
		if req.Title != "" || req.Message != "" {
			payload, err = json.Marshal(map[string]string{"title": req.Title, "message": req.Message})
			if err != nil {
				return playback.TerminationResult{}, err
			}
		}
		command, err := playback.NewCommandEnvelope(sessionID, commandID, playback.CommandTerminate, payload)
		if err != nil {
			return playback.TerminationResult{}, err
		}
		command.Reason = req.Reason
		command.ReasonCode = req.ReasonCode
		command.IssuedBy = &playback.CommandIssuedBy{Kind: "admin"}
		command.DeadlineMS = deadlineMS

		status, err := terminator.TerminateSessionGenerationSnapshotStatus(r.Context(), sessionID, generation, func(snapshot *playback.Session) error {
			if snapshot == nil {
				return nil
			}
			finalizeCtx, cancel := context.WithTimeout(context.Background(), playbackFinalizationTimeout)
			defer cancel()
			if h.playback.RealtimeHub != nil {
				if sendErr := h.playback.RealtimeHub.Send(sessionID, command); sendErr != nil && !errors.Is(sendErr, playback.ErrRealtimeConnectionNotFound) {
					slog.WarnContext(finalizeCtx, "realtime termination delivery failed; completing server-side fallback", "session", sessionID, "generation", generation, "error", sendErr)
				}
			}
			h.playback.finalizeSessionStop(finalizeCtx, snapshot, true, "terminate", true)
			return nil
		})
		if err != nil {
			return playback.TerminationResult{}, err
		}
		return playback.TerminationResult{Status: status, CommandID: commandID}, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, playback.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_key_reused", "Idempotency key was reused with different termination input")
		case errors.Is(err, playback.ErrIdempotencyCapacity):
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Termination idempotency capacity is temporarily exhausted")
		case errors.Is(err, playback.ErrSnapshotUnknown), errors.Is(err, playback.ErrSnapshotStale), errors.Is(err, playback.ErrSnapshotIncomplete):
			writeError(w, http.StatusConflict, "snapshot_unavailable", "A complete fresh session snapshot is required")
		case errors.Is(err, playback.ErrSnapshotIdentityMismatch), errors.Is(err, playback.ErrSessionSuperseded):
			writeError(w, http.StatusConflict, "session_generation_conflict", "Playback session generation no longer matches the snapshot")
		case errors.Is(err, playback.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Playback session not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to terminate playback session")
		}
		return
	}
	writeJSON(w, http.StatusOK, playbackControlResponse{CommandID: result.CommandID, Status: string(result.Status)})
}

func validPlaybackControlFields(sessionID string, req playbackControlRequest) bool {
	return len(sessionID) <= 512 &&
		len(req.SessionGeneration) <= 128 &&
		len(req.SnapshotID) <= 128 &&
		len(req.IdempotencyKey) <= 255 &&
		len(req.ReasonCode) <= 128 &&
		utf8.RuneCountInString(req.Reason) <= 1024 &&
		utf8.RuneCountInString(req.Title) <= 256 &&
		utf8.RuneCountInString(req.Message) <= 4096
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

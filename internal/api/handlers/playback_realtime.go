package handlers

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type playbackCommandRecord struct {
	SessionID string
	Name      playback.CommandName
}

// stopPlaybackSession stops a session. userInitiated must be true only for a
// genuine user/admin stop (the recipe card is then deleted); false for system
// teardown such as an ffmpeg-exit cleanup, where the card is kept so the client
// can reconstruct.
func (h *PlaybackHandler) stopPlaybackSession(ctx context.Context, session *playback.Session, userInitiated bool) error {
	if h == nil || session == nil || session.ID == "" {
		return playback.ErrSessionNotFound
	}

	if err := h.sessionMgr.StopSession(session.ID); err != nil {
		return err
	}
	h.finalizeSessionStop(ctx, session, true, "stop", userInitiated)
	return nil
}

func (h *PlaybackHandler) stopPlaybackSessionByID(ctx context.Context, sessionID string, userInitiated bool) error {
	if h == nil || sessionID == "" {
		return playback.ErrSessionNotFound
	}
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		return err
	}
	return h.stopPlaybackSession(ctx, session, userInitiated)
}

func (h *PlaybackHandler) abortPlaybackSession(ctx context.Context, session *playback.Session) error {
	if h == nil || session == nil || session.ID == "" {
		return playback.ErrSessionNotFound
	}

	if err := h.sessionMgr.StopSession(session.ID); err != nil {
		return err
	}
	h.finalizeSessionAbort(ctx, session, true, "abort")
	return nil
}

func (h *PlaybackHandler) abortPlaybackSessionByID(ctx context.Context, sessionID string) error {
	if h == nil || sessionID == "" {
		return playback.ErrSessionNotFound
	}
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		return err
	}
	return h.abortPlaybackSession(ctx, session)
}

func (h *PlaybackHandler) rememberRealtimeCommand(commandID, sessionID string, name playback.CommandName) {
	if h == nil || commandID == "" || sessionID == "" {
		return
	}

	h.realtimeCommandMu.Lock()
	h.realtimeCommands[commandID] = playbackCommandRecord{
		SessionID: sessionID,
		Name:      name,
	}
	h.realtimeCommandMu.Unlock()
}

func (h *PlaybackHandler) forgetRealtimeCommand(commandID string) {
	if h == nil || commandID == "" {
		return
	}

	h.realtimeCommandMu.Lock()
	delete(h.realtimeCommands, commandID)
	h.realtimeCommandMu.Unlock()
}

func (h *PlaybackHandler) getRealtimeCommand(commandID string) (playbackCommandRecord, bool) {
	if h == nil || commandID == "" {
		return playbackCommandRecord{}, false
	}

	h.realtimeCommandMu.Lock()
	record, ok := h.realtimeCommands[commandID]
	h.realtimeCommandMu.Unlock()
	return record, ok
}

func (h *PlaybackHandler) setRealtimeConnectionState(sessionID string, connected bool) bool {
	if h == nil || sessionID == "" {
		return false
	}

	type realtimeStateSetter interface {
		SetRealtimeConnection(sessionID string, connected bool) error
	}

	mgr, ok := h.sessionMgr.(realtimeStateSetter)
	if !ok {
		return false
	}

	session, err := h.sessionMgr.GetSession(sessionID)
	if err == nil && session != nil && session.HasRealtimeConnection == connected {
		return false
	}

	if err := mgr.SetRealtimeConnection(sessionID, connected); err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			return false
		}
		slog.Warn("failed to update realtime connection state", "session", sessionID, "connected", connected, "error", err)
		return false
	}
	return true
}

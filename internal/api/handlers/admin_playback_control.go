package handlers

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	defaultPlaybackControlDeadline = 3 * time.Second
	maxPlaybackControlDeadline     = 10 * time.Second
)

type AdminPlaybackControlHandler struct {
	playback *PlaybackHandler

	// commandLedgers holds the per-session sequenced command receipts the v2
	// port applies once (see admin_playback_commands.go). The frozen v1
	// handlers above never consult it.
	commandMu      sync.Mutex
	commandLedgers map[string]*adminPlaybackCommandLedger
	commandSweptAt time.Time

	// terminateLocks serialize v2 terminates per session
	// (admin_playback_terminate.go). An entry lives only while a terminate
	// holds or waits for it.
	terminateMu    sync.Mutex
	terminateLocks map[string]*adminTerminateLock
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

// endSessionForAdmin is the fallback when the player does not confirm an
// administrator command in time: the session stops here and its attempt is
// stopped as ended by an administrator, so a player that missed the command
// is told to exit (410) when it next reports progress or replans, instead of
// renewing into a fresh session.
func (h *AdminPlaybackControlHandler) endSessionForAdmin(sessionID string) error {
	ctx := context.Background()
	err := h.playback.stopPlaybackSessionByID(ctx, sessionID, true)
	if err == nil || errors.Is(err, playback.ErrSessionNotFound) {
		h.playback.markAttemptStoppedByAdmin(ctx, sessionID)
	}
	return err
}

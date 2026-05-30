package playback

import (
	"context"
	"errors"
	"log/slog"
)

type subtitleReadySessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

// SubtitleReadyNotifier pushes "subtitle ready" events to active playback
// sessions when a generated subtitle track (AI translation, later ASR) becomes
// available, so the player can refresh and select it without a manual reload.
//
// It satisfies the subtitles/ai Notifier interface structurally, keeping the ai
// package free of any playback dependency.
type SubtitleReadyNotifier struct {
	sessions subtitleReadySessionLookup
	hub      *RealtimeHub
}

// NewSubtitleReadyNotifier returns a notifier, or nil if its dependencies are
// missing (callers treat a nil notifier as a no-op).
func NewSubtitleReadyNotifier(sessions subtitleReadySessionLookup, hub *RealtimeHub) *SubtitleReadyNotifier {
	if sessions == nil || hub == nil {
		return nil
	}
	return &SubtitleReadyNotifier{sessions: sessions, hub: hub}
}

// SubtitleReady notifies active sessions for the file that a new subtitle track
// with the given downloaded-subtitle ID is available.
func (n *SubtitleReadyNotifier) SubtitleReady(_ context.Context, mediaFileID, subtitleID int, language, label string) {
	if n == nil || mediaFileID <= 0 || subtitleID <= 0 {
		return
	}

	for _, session := range n.sessions.GetSessionsByMediaFileID(mediaFileID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		event, err := NewSubtitleReadyEvent(session.ID, mediaFileID, subtitleID, language, label)
		if err != nil {
			slog.Warn("failed to encode subtitle ready realtime event",
				"session_id", session.ID, "file_id", mediaFileID, "subtitle_id", subtitleID, "error", err)
			continue
		}
		if err := n.hub.Send(session.ID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
			slog.Warn("failed to deliver subtitle ready realtime event",
				"session_id", session.ID, "file_id", mediaFileID, "subtitle_id", subtitleID, "error", err)
		}
	}
}

// TranslationStarted tells one session a live translation has begun.
func (n *SubtitleReadyNotifier) TranslationStarted(_ context.Context, sessionID string, fileID int, jobID int64, trackKey, language, label string, totalCues int) {
	n.sendTranslation(sessionID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationStartedEvent(sessionID, fileID, jobID, trackKey, language, label, totalCues)
	})
}

// TranslationCues pushes a batch of translated cues to one session.
func (n *SubtitleReadyNotifier) TranslationCues(_ context.Context, sessionID string, fileID int, jobID int64, trackKey string, cues []StreamCue, done, total int) {
	n.sendTranslation(sessionID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationCuesEvent(sessionID, fileID, jobID, trackKey, cues, done, total)
	})
}

// TranslationCompleted tells one session a live translation finished.
func (n *SubtitleReadyNotifier) TranslationCompleted(_ context.Context, sessionID string, fileID int, jobID int64, trackKey string, subtitleID int, language, label string) {
	n.sendTranslation(sessionID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationCompletedEvent(sessionID, fileID, jobID, trackKey, subtitleID, language, label)
	})
}

// TranslationFailed tells one session a live translation failed.
func (n *SubtitleReadyNotifier) TranslationFailed(_ context.Context, sessionID string, fileID int, jobID int64, trackKey, message string) {
	n.sendTranslation(sessionID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationFailedEvent(sessionID, fileID, jobID, trackKey, message)
	})
}

// sendTranslation builds and delivers a translation event to a single session.
func (n *SubtitleReadyNotifier) sendTranslation(sessionID string, build func() (EventEnvelope, error)) {
	if n == nil || n.hub == nil || sessionID == "" {
		return
	}
	event, err := build()
	if err != nil {
		slog.Warn("failed to encode subtitle translation realtime event", "session_id", sessionID, "error", err)
		return
	}
	if err := n.hub.Send(sessionID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
		slog.Warn("failed to deliver subtitle translation realtime event", "session_id", sessionID, "error", err)
	}
}

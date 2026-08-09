package playback

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/models"
)

type subtitleReadySessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

// SubtitleInventoryResolver resolves the frozen combined-ordinal inventory for
// a file so realtime events can name a new track by its ordinal, identity, and
// stream URL rather than leaving the client to reconstruct one.
type SubtitleInventoryResolver interface {
	MediaFile(ctx context.Context, fileID int) (*models.MediaFile, error)
	AdditionalSubtitles(ctx context.Context, file *models.MediaFile) ([]SubtitleInventoryEntryV3, error)
}

// SubtitleReadyNotifier pushes "subtitle ready" events to active playback
// sessions when a generated subtitle track (AI translation, later ASR) becomes
// available, so the player can refresh and select it without a manual reload.
//
// It satisfies the subtitles/ai Notifier interface structurally, keeping the ai
// package free of any playback dependency.
type SubtitleReadyNotifier struct {
	sessions  subtitleReadySessionLookup
	hub       *RealtimeHub
	inventory SubtitleInventoryResolver
}

// NewSubtitleReadyNotifier returns a notifier, or nil if its dependencies are
// missing (callers treat a nil notifier as a no-op). A nil inventory resolver
// is allowed: events then omit the track block and the client refetches its
// plan to learn the new ordinal.
func NewSubtitleReadyNotifier(sessions subtitleReadySessionLookup, hub *RealtimeHub, inventory SubtitleInventoryResolver) *SubtitleReadyNotifier {
	if sessions == nil || hub == nil {
		return nil
	}
	return &SubtitleReadyNotifier{sessions: sessions, hub: hub, inventory: inventory}
}

// SubtitleReady notifies active sessions for the file that a new subtitle track
// with the given downloaded-subtitle ID is available.
func (n *SubtitleReadyNotifier) SubtitleReady(ctx context.Context, mediaFileID, subtitleID int, language, label string) {
	if n == nil || mediaFileID <= 0 || subtitleID <= 0 {
		return
	}

	for _, session := range n.sessions.GetSessionsByMediaFileID(mediaFileID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		track := n.resolveTrack(ctx, session.ID, mediaFileID, subtitleID)
		event, err := NewSubtitleReadyEvent(session.ID, mediaFileID, subtitleID, language, label, track)
		if err != nil {
			slog.WarnContext(ctx, "failed to encode subtitle ready realtime event", "component", "playback",
				"session_id", session.ID, "file_id", mediaFileID, "subtitle_id", subtitleID, "error", err)
			continue
		}
		if err := n.hub.Send(session.ID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
			slog.WarnContext(ctx, "failed to deliver subtitle ready realtime event", "component", "playback",
				"session_id", session.ID, "file_id", mediaFileID, "subtitle_id", subtitleID, "error", err)
		}
	}
}

// TranslationStarted tells one session a live translation has begun.
func (n *SubtitleReadyNotifier) TranslationStarted(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey, language, label string, totalCues int) {
	n.sendTranslation(sessionID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationStartedEvent(sessionID, fileID, jobID, trackKey, language, label, totalCues)
	})
}

// TranslationCues pushes a batch of translated cues to one session.
func (n *SubtitleReadyNotifier) TranslationCues(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey string, cues []StreamCue, done, total int) {
	n.sendTranslation(sessionID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationCuesEvent(sessionID, fileID, jobID, trackKey, cues, done, total)
	})
}

// TranslationCompleted tells one session a live translation finished.
func (n *SubtitleReadyNotifier) TranslationCompleted(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey string, subtitleID int, language, label string) {
	track := n.resolveTrack(ctx, sessionID, fileID, subtitleID)
	n.sendTranslation(sessionID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationCompletedEvent(sessionID, fileID, jobID, trackKey, subtitleID, language, label, track)
	})
}

// TranslationFailed tells one session a live translation failed.
func (n *SubtitleReadyNotifier) TranslationFailed(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey, message string) {
	n.sendTranslation(sessionID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationFailedEvent(sessionID, fileID, jobID, trackKey, message)
	})
}

// resolveTrack looks up the newly persisted track's inventory entry. The
// subtitle row is already committed when these events fire, so the entry it
// resolves to carries the same ordinal the next plan will publish.
//
// The row ID is server-private but retained on each inventory item, so match it
// exactly. Language/label are presentation metadata and need not be unique.
func (n *SubtitleReadyNotifier) resolveTrack(ctx context.Context, sessionID string, fileID, subtitleID int) *SubtitleInventoryItemV3 {
	if n == nil || n.inventory == nil || fileID <= 0 {
		return nil
	}
	file, err := n.inventory.MediaFile(ctx, fileID)
	if err != nil || file == nil {
		slog.WarnContext(ctx, "subtitle realtime event omits track identity", "component", "playback",
			"file_id", fileID, "error", err)
		return nil
	}
	additional, err := n.inventory.AdditionalSubtitles(ctx, file)
	if err != nil {
		slog.WarnContext(ctx, "subtitle realtime event omits track identity", "component", "playback",
			"file_id", fileID, "error", err)
		return nil
	}
	items := SubtitleInventoryV3(sessionID, file, additional)
	for i := range items {
		if items[i].Source != SubtitleSourceDownloadedV3 {
			continue
		}
		if items[i].downloadedSubtitleID == subtitleID {
			track := items[i]
			return &track
		}
	}
	return nil
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

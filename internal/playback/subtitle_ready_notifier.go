package playback

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/google/uuid"
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
	// SessionClientFeatures returns the client features that pick the
	// session's sidecar representations (SubtitleSidecarExtV3), so an event
	// publishes the same URL as the session's plans. nil selects the defaults.
	// An error means the representation is unknown; the event then omits the
	// track rather than guess.
	SessionClientFeatures(ctx context.Context, sessionID string) ([]string, error)
}

// SubtitleReadyNotifier pushes "subtitle ready" events to active playback
// sessions when a generated subtitle track (AI translation, later ASR) becomes
// available, so the player can refresh and select it without a manual reload.
//
// It satisfies the subtitles/ai Notifier interface structurally, keeping the ai
// package free of any playback dependency.
type SubtitleReadyNotifier struct {
	sessions           subtitleReadySessionLookup
	hub                *RealtimeHub
	inventory          SubtitleInventoryResolver
	translationSession *Session

	// Timing changes and sync progress reach sessions on other API servers
	// through the event bus (UseEventBus); sourceID drops this server's own
	// messages. The bus is shared by the copies BindTranslation makes.
	sourceID string
	bus      *timingBus
}

type timingBus struct {
	mu      sync.RWMutex
	publish func(context.Context, RealtimeEventName, string) error
}

// SubtitleSyncUpdate is a sync job's state for the players of a file.
type SubtitleSyncUpdate struct {
	FileID int
	// SubtitleID names a stored subtitle; 0 for a sidecar.
	SubtitleID int
	SyncKey    string
	Timing     SubtitleSyncTiming
	Job        SubtitleSyncJob
}

// subtitleSyncMessage is a sync job update sent to the other API servers.
type subtitleSyncMessage struct {
	SourceID   string             `json:"source_id"`
	FileID     int                `json:"file_id"`
	SubtitleID int                `json:"subtitle_id,omitempty"`
	SyncKey    string             `json:"sync_key"`
	Timing     SubtitleSyncTiming `json:"timing"`
	Job        SubtitleSyncJob    `json:"job"`
}

// subtitleTimingMessage is a timing change sent to the other API servers.
// SubtitleID names a stored subtitle; SyncKey names either kind, and is
// absent from servers that predate sidecar sync.
type subtitleTimingMessage struct {
	SourceID   string `json:"source_id"`
	FileID     int    `json:"file_id"`
	SubtitleID int    `json:"subtitle_id,omitempty"`
	SyncKey    string `json:"sync_key,omitempty"`
}

// timingChange identifies a retimed subtitle within a file.
type timingChange struct {
	fileID     int
	subtitleID int // stored subtitles only
	syncKey    string
}

func (c timingChange) valid() bool { return c.fileID > 0 && c.syncKey != "" }

// NewSubtitleReadyNotifier returns a notifier, or nil if its dependencies are
// missing (callers treat a nil notifier as a no-op). A nil inventory resolver
// is allowed: events then omit the track block and the client refetches its
// plan to learn the new ordinal.
func NewSubtitleReadyNotifier(sessions subtitleReadySessionLookup, hub *RealtimeHub, inventory SubtitleInventoryResolver) *SubtitleReadyNotifier {
	if sessions == nil || hub == nil {
		return nil
	}
	return &SubtitleReadyNotifier{sessions: sessions, hub: hub, inventory: inventory, sourceID: uuid.NewString(), bus: &timingBus{}}
}

// UseEventBus delivers timing changes and sync updates to sessions on every
// API server. Messages travel under the realtime event name they produce.
// Call it once during startup with the server lifetime context; repeated
// calls do not add subscriptions.
func (n *SubtitleReadyNotifier) UseEventBus(
	ctx context.Context,
	publish func(context.Context, RealtimeEventName, string) error,
	subscribe func(context.Context, func(RealtimeEventName, string)) error,
) error {
	if n == nil || n.bus == nil || publish == nil || subscribe == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	n.bus.mu.Lock()
	defer n.bus.mu.Unlock()
	if n.bus.publish != nil {
		return nil
	}
	if err := subscribe(ctx, func(event RealtimeEventName, payload string) {
		if ctx.Err() != nil {
			return
		}
		if event == RealtimeEventSubtitleSyncUpdated {
			var msg subtitleSyncMessage
			if json.Unmarshal([]byte(payload), &msg) != nil || msg.SourceID == "" || msg.SourceID == n.sourceID {
				return
			}
			n.dispatchSyncUpdated(ctx, SubtitleSyncUpdate{FileID: msg.FileID, SubtitleID: msg.SubtitleID,
				SyncKey: msg.SyncKey, Timing: msg.Timing, Job: msg.Job})
			return
		}
		var msg subtitleTimingMessage
		if json.Unmarshal([]byte(payload), &msg) != nil || msg.SourceID == "" || msg.SourceID == n.sourceID {
			return
		}
		change := timingChange{fileID: msg.FileID, subtitleID: msg.SubtitleID, syncKey: msg.SyncKey}
		if change.syncKey == "" && change.subtitleID > 0 {
			change.syncKey = subtitles.StoredSyncKey(change.subtitleID)
		}
		n.dispatchTimingChanged(ctx, change)
	}); err != nil {
		return err
	}
	n.bus.publish = publish
	return nil
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
		track := n.resolveTrack(ctx, session.ID, mediaFileID, downloadedTrack(subtitleID))
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

// SubtitleTimingChanged tells active sessions for the file, on every API
// server, that a stored subtitle or a sidecar was retimed, so a player
// showing it fetches it again.
func (n *SubtitleReadyNotifier) SubtitleTimingChanged(ctx context.Context, target subtitles.SyncTarget) {
	change := timingChange{fileID: target.MediaFileID, subtitleID: target.StoredID, syncKey: target.Key()}
	if n == nil || !change.valid() {
		return
	}
	n.dispatchTimingChanged(ctx, change)
	n.publish(ctx, RealtimeEventSubtitleTimingChanged, subtitleTimingMessage{SourceID: n.sourceID, FileID: change.fileID,
		SubtitleID: change.subtitleID, SyncKey: change.syncKey})
}

// SubtitleSyncUpdated tells active sessions for the file, on every API
// server, how a sync job of one of its subtitles is going.
func (n *SubtitleReadyNotifier) SubtitleSyncUpdated(ctx context.Context, update SubtitleSyncUpdate) {
	if n == nil || update.FileID <= 0 || update.SyncKey == "" {
		return
	}
	n.dispatchSyncUpdated(ctx, update)
	n.publish(ctx, RealtimeEventSubtitleSyncUpdated, subtitleSyncMessage{SourceID: n.sourceID, FileID: update.FileID,
		SubtitleID: update.SubtitleID, SyncKey: update.SyncKey, Timing: update.Timing, Job: update.Job})
}

// publish sends a message to the other API servers, when a bus is set.
func (n *SubtitleReadyNotifier) publish(ctx context.Context, event RealtimeEventName, message any) {
	if n.bus == nil {
		return
	}
	n.bus.mu.RLock()
	publish := n.bus.publish
	n.bus.mu.RUnlock()
	if publish == nil {
		return
	}
	payload, err := json.Marshal(message)
	if err == nil {
		publishCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = publish(publishCtx, event, string(payload))
		cancel()
	}
	if err != nil {
		slog.WarnContext(ctx, "failed to publish subtitle event", "component", "playback", "event", event, "error", err)
	}
}

// dispatchSyncUpdated sends a sync update to this server's sessions of the
// file.
func (n *SubtitleReadyNotifier) dispatchSyncUpdated(ctx context.Context, update SubtitleSyncUpdate) {
	for _, session := range n.sessions.GetSessionsByMediaFileID(update.FileID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		event, err := NewSubtitleSyncUpdatedEvent(SubtitleSyncUpdatedPayload{
			SessionID: session.ID, FileID: update.FileID, SyncKey: update.SyncKey, SubtitleID: update.SubtitleID,
			Timing: update.Timing, Job: update.Job,
		})
		if err != nil {
			slog.WarnContext(ctx, "failed to encode subtitle sync realtime event", "component", "playback",
				"session_id", session.ID, "file_id", update.FileID, "sync_key", update.SyncKey, "error", err)
			continue
		}
		if err := n.hub.Send(session.ID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
			slog.WarnContext(ctx, "failed to deliver subtitle sync realtime event", "component", "playback",
				"session_id", session.ID, "file_id", update.FileID, "sync_key", update.SyncKey, "error", err)
		}
	}
}

// dispatchTimingChanged sends the event to this server's sessions of the file.
func (n *SubtitleReadyNotifier) dispatchTimingChanged(ctx context.Context, change timingChange) {
	if !change.valid() {
		return
	}
	for _, session := range n.sessions.GetSessionsByMediaFileID(change.fileID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		track := n.resolveTrack(ctx, session.ID, change.fileID, func(item SubtitleInventoryItemV3) bool {
			return item.SyncKey == change.syncKey
		})
		event, err := NewSubtitleTimingChangedEvent(session.ID, change.fileID, change.subtitleID, change.syncKey, track)
		if err != nil {
			slog.WarnContext(ctx, "failed to encode subtitle timing realtime event", "component", "playback",
				"session_id", session.ID, "file_id", change.fileID, "sync_key", change.syncKey, "error", err)
			continue
		}
		if err := n.hub.Send(session.ID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
			slog.WarnContext(ctx, "failed to deliver subtitle timing realtime event", "component", "playback",
				"session_id", session.ID, "file_id", change.fileID, "sync_key", change.syncKey, "error", err)
		}
	}
}

// TranslationStarted tells one session a live translation has begun.
func (n *SubtitleReadyNotifier) TranslationStarted(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey, language, label string, totalCues int) {
	n.sendTranslation(sessionID, fileID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationStartedEvent(sessionID, fileID, jobID, trackKey, language, label, totalCues)
	})
}

// TranslationCues pushes a batch of translated cues to one session.
func (n *SubtitleReadyNotifier) TranslationCues(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey string, cues []StreamCue, done, total int) {
	n.sendTranslation(sessionID, fileID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationCuesEvent(sessionID, fileID, jobID, trackKey, cues, done, total)
	})
}

// TranslationCompleted tells one session a live translation finished.
func (n *SubtitleReadyNotifier) TranslationCompleted(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey string, subtitleID int, language, label string) {
	track := n.resolveTrack(ctx, sessionID, fileID, downloadedTrack(subtitleID))
	n.sendTranslation(sessionID, fileID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationCompletedEvent(sessionID, fileID, jobID, trackKey, subtitleID, language, label, track)
	})
}

// TranslationFailed tells one session a live translation failed.
func (n *SubtitleReadyNotifier) TranslationFailed(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey, message string) {
	n.sendTranslation(sessionID, fileID, func() (EventEnvelope, error) {
		return NewSubtitleTranslationFailedEvent(sessionID, fileID, jobID, trackKey, message)
	})
}

// resolveTrack looks up the newly persisted track's inventory entry. The
// subtitle row is already committed when these events fire, so the entry it
// resolves to carries the same ordinal the next plan will publish.
//
// The row ID is server-private but retained on each inventory item, so match it
// exactly. Language/label are presentation metadata and need not be unique.
func (n *SubtitleReadyNotifier) resolveTrack(ctx context.Context, sessionID string, fileID int, match func(SubtitleInventoryItemV3) bool) *SubtitleInventoryItemV3 {
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
	features, err := n.inventory.SessionClientFeatures(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "subtitle realtime event omits track identity", "component", "playback",
			"file_id", fileID, "error", err)
		return nil
	}
	items := ScopeSubtitleInventoryV3(sessionID, file, BuildSubtitleInventoryV3(file, additional), features)
	for i := range items {
		if match(items[i]) {
			track := items[i]
			return &track
		}
	}
	return nil
}

// downloadedTrack matches the inventory entry of a stored subtitle row.
func downloadedTrack(subtitleID int) func(SubtitleInventoryItemV3) bool {
	return func(item SubtitleInventoryItemV3) bool {
		return item.Source == SubtitleSourceDownloadedV3 && item.downloadedSubtitleID == subtitleID
	}
}

// sendTranslation builds and delivers a translation event to a single session.
func (n *SubtitleReadyNotifier) sendTranslation(sessionID string, fileID int, build func() (EventEnvelope, error)) {
	if n == nil || n.hub == nil || sessionID == "" || !n.translationSessionMatches(sessionID, fileID) {
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

package playback

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
)

const defaultChapterThumbnailEventTTL = 15 * time.Minute

type chapterThumbnailSessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

type ChapterThumbnailURLResolver func(context.Context, string, time.Duration) (string, error)

// ChapterThumbnailNotifier publishes live chapter thumbnail updates to active playback sessions.
type ChapterThumbnailNotifier struct {
	sessions chapterThumbnailSessionLookup
	hub      *RealtimeHub
	presign  ChapterThumbnailURLResolver
	ttl      time.Duration
	attempts PlanStoreV3
	sourceID string
	mu       sync.RWMutex
	publish  func(context.Context, string) error
}

// NewChapterThumbnailNotifier creates a notifier that targets active sessions for a file.
func NewChapterThumbnailNotifier(
	sessions chapterThumbnailSessionLookup,
	hub *RealtimeHub,
	presign ChapterThumbnailURLResolver,
	ttl time.Duration,
	attempts ...PlanStoreV3,
) *ChapterThumbnailNotifier {
	if sessions == nil || hub == nil || presign == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = defaultChapterThumbnailEventTTL
	}
	var store PlanStoreV3
	if len(attempts) > 0 {
		store = attempts[0]
	}
	return &ChapterThumbnailNotifier{
		attempts: store,
		sourceID: uuid.NewString(),
		sessions: sessions,
		hub:      hub,
		presign:  presign,
		ttl:      ttl,
	}
}

// ChapterThumbnailReady notifies active sessions that a chapter thumbnail is now available.
func (n *ChapterThumbnailNotifier) ChapterThumbnailReady(
	ctx context.Context,
	fileID int,
	chapterIndex int,
	thumbnailPath string,
	thumbnailThumbhash string,
) {
	if n == nil || fileID <= 0 || chapterIndex < 0 || thumbnailPath == "" {
		return
	}

	thumbnailURL, err := n.presign(ctx, thumbnailPath, n.ttl)
	if err != nil {
		slog.WarnContext(ctx,
			"failed to presign chapter thumbnail for realtime event", "component", "playback",
			"file_id",
			fileID,
			"chapter_index",
			chapterIndex,
			"error",
			err,
		)
		return
	}

	for _, session := range n.sessions.GetSessionsByMediaFileID(fileID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		event, err := NewChapterThumbnailReadyEvent(
			session.ID,
			fileID,
			chapterIndex,
			thumbnailURL,
			thumbnailThumbhash,
		)
		if err != nil {
			slog.WarnContext(ctx,
				"failed to encode chapter thumbnail realtime event", "component", "playback",
				"session_id",
				session.ID,
				"file_id",
				fileID,
				"chapter_index",
				chapterIndex,
				"error",
				err,
			)
			continue
		}
		if err := n.hub.Send(session.ID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
			slog.WarnContext(ctx,
				"failed to deliver chapter thumbnail realtime event", "component", "playback",
				"session_id",
				session.ID,
				"file_id",
				fileID,
				"chapter_index",
				chapterIndex,
				"error",
				err,
			)
		}
	}
}

// MarkerThumbnailReady never sends a new event to frozen or unnegotiated clients.
func (n *ChapterThumbnailNotifier) MarkerThumbnailReady(ctx context.Context, fileID int, marker models.MarkerThumbnail) {
	if n == nil || fileID <= 0 || marker.ThumbnailPath == "" || ctx.Err() != nil || !(models.MarkerSegment{Kind: marker.Kind, StartSeconds: marker.StartSeconds, EndSeconds: marker.EndSeconds}).Valid() {
		return
	}
	n.dispatchMarker(ctx, fileID, marker)
	n.mu.RLock()
	publish := n.publish
	n.mu.RUnlock()
	if publish != nil && ctx.Err() == nil {
		payload, err := json.Marshal(markerThumbnailSnapshot{SourceID: n.sourceID, FileID: fileID, Marker: marker})
		if err == nil {
			publishCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err = publish(publishCtx, string(payload))
			cancel()
		}
		if err != nil {
			slog.WarnContext(ctx, "publish marker thumbnail failed", "component", "playback", "file_id", fileID, "error", err)
		}
	}
}

func (n *ChapterThumbnailNotifier) dispatchMarker(ctx context.Context, fileID int, marker models.MarkerThumbnail) {
	if n.attempts == nil {
		return
	}
	url, err := n.presign(ctx, marker.ThumbnailPath, n.ttl)
	if err != nil || url == "" {
		return
	}
	for _, session := range n.sessions.GetSessionsByMediaFileID(fileID) {
		if session == nil || !session.HasRealtimeConnection {
			continue
		}
		record, err := n.attempts.GetAttempt(ctx, session.ID)
		if err != nil || record == nil || !HasFeatureV3(record.NormalizedRequest.ClientFeatures, FeatureMarkerThumbnailsV3) {
			continue
		}
		data, err := json.Marshal(MarkerThumbnailReadyPayload{SessionID: session.ID, FileID: fileID, MarkerSegment: models.MarkerSegment{Kind: marker.Kind, StartSeconds: marker.StartSeconds, EndSeconds: marker.EndSeconds}, ThumbnailURL: url, ThumbnailThumbhash: marker.ThumbnailThumbhash, ThumbnailCaptureSeconds: marker.StartSeconds})
		if err != nil {
			continue
		}
		event, err := NewEventEnvelope(session.ID, RealtimeEventMarkerThumbnailReady, data)
		if err != nil {
			continue
		}
		if err := n.hub.Send(session.ID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
			slog.WarnContext(ctx, "failed to deliver marker thumbnail", "component", "playback", "file_id", fileID, "session_id", session.ID, "error", err)
		}
	}
}

// The bus carries an immutable object key, not a URL. Each serving replica
// obtains a protected URL and applies its own session feature negotiation.
type markerThumbnailSnapshot struct {
	SourceID string                 `json:"source_id"`
	FileID   int                    `json:"file_id"`
	Marker   models.MarkerThumbnail `json:"marker"`
}

// UseMarkerEventBus enables marker readiness delivery across API replicas once.
func (n *ChapterThumbnailNotifier) UseMarkerEventBus(ctx context.Context, publish func(context.Context, string) error, subscribe func(context.Context, func(string)) error) error {
	if n == nil || publish == nil || subscribe == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.publish != nil {
		return nil
	}
	if err := subscribe(ctx, func(payload string) {
		if ctx.Err() != nil {
			return
		}
		var snapshot markerThumbnailSnapshot
		if err := json.Unmarshal([]byte(payload), &snapshot); err != nil || snapshot.SourceID == "" || snapshot.SourceID == n.sourceID || snapshot.FileID <= 0 || snapshot.Marker.ThumbnailPath == "" {
			return
		}
		segment := models.MarkerSegment{Kind: snapshot.Marker.Kind, StartSeconds: snapshot.Marker.StartSeconds, EndSeconds: snapshot.Marker.EndSeconds}
		if !segment.Valid() {
			return
		}
		n.dispatchMarker(ctx, snapshot.FileID, snapshot.Marker)
	}); err != nil {
		return err
	}
	n.publish = publish
	return nil
}

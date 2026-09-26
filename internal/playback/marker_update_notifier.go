package playback

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/google/uuid"
)

type markerUpdateSessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

// MarkerUpdateNotifier publishes live marker updates to active playback sessions.
type MarkerUpdateNotifier struct {
	sessions markerUpdateSessionLookup
	hub      *RealtimeHub
	sourceID string

	mu      sync.RWMutex
	publish func(context.Context, string) error
}

// markerUpdateSnapshot carries enough data to deliver an update without reading
// the database: on-demand markers may never be persisted.
type markerUpdateSnapshot struct {
	SourceID string                 `json:"source_id"`
	FileID   int                    `json:"file_id"`
	Segments []models.MarkerSegment `json:"marker_segments"`
}

func NewMarkerUpdateNotifier(sessions markerUpdateSessionLookup, hub *RealtimeHub) *MarkerUpdateNotifier {
	if sessions == nil || hub == nil {
		return nil
	}
	return &MarkerUpdateNotifier{
		sessions: sessions,
		hub:      hub,
		sourceID: uuid.NewString(),
	}
}

// UseEventBus enables cross-replica delivery. Call it once during startup with
// the server lifetime context. Repeated calls do not create more subscriptions.
func (n *MarkerUpdateNotifier) UseEventBus(
	ctx context.Context,
	publish func(context.Context, string) error,
	subscribe func(context.Context, func(string)) error,
) error {
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
		var snapshot markerUpdateSnapshot
		if err := json.Unmarshal([]byte(payload), &snapshot); err != nil ||
			snapshot.SourceID == "" || snapshot.SourceID == n.sourceID || snapshot.FileID <= 0 {
			return
		}
		snapshot.Segments = models.EffectiveMarkerSegments(&models.MediaFile{MarkerSegments: snapshot.Segments})
		n.dispatch(ctx, snapshot)
	}); err != nil {
		return err
	}
	n.publish = publish
	return nil
}

func (n *MarkerUpdateNotifier) MarkersUpdated(ctx context.Context, file *models.MediaFile) {
	if n == nil || file == nil || file.ID <= 0 || ctx.Err() != nil {
		return
	}

	snapshot := markerUpdateSnapshot{
		SourceID: n.sourceID,
		FileID:   file.ID,
		Segments: models.EffectiveMarkerSegments(file),
	}
	n.dispatch(ctx, snapshot)
	if ctx.Err() != nil {
		return
	}
	n.mu.RLock()
	publish := n.publish
	n.mu.RUnlock()
	if publish != nil {
		payload, err := json.Marshal(snapshot)
		if err == nil {
			publishCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err = publish(publishCtx, string(payload))
			cancel()
		}
		if err != nil {
			slog.WarnContext(ctx, "failed to publish marker update", "component", "playback", "file_id", file.ID, "error", err)
		}
	}
}

func (n *MarkerUpdateNotifier) dispatch(ctx context.Context, snapshot markerUpdateSnapshot) {
	firstRange := func(kind string) *TimeRangePayload {
		for _, segment := range snapshot.Segments {
			if segment.Kind == kind {
				return &TimeRangePayload{Start: segment.StartSeconds, End: segment.EndSeconds}
			}
		}
		return nil
	}
	intro, credits := firstRange("intro"), firstRange("credits")
	recap, preview := firstRange("recap"), firstRange("preview")
	for _, session := range n.sessions.GetSessionsByMediaFileID(snapshot.FileID) {
		if ctx.Err() != nil {
			return
		}
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		event, err := NewMarkersUpdatedEvent(session.ID, snapshot.FileID, intro, credits, recap, preview, snapshot.Segments...)
		if err != nil {
			slog.WarnContext(ctx,
				"failed to encode markers updated realtime event", "component", "playback",
				"session_id",
				session.ID,
				"file_id",
				snapshot.FileID,
				"error",
				err,
			)
			continue
		}
		if err := n.hub.Send(session.ID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
			slog.WarnContext(ctx,
				"failed to deliver markers updated realtime event", "component", "playback",
				"session_id",
				session.ID,
				"file_id",
				snapshot.FileID,
				"error",
				err,
			)
		}
	}
}

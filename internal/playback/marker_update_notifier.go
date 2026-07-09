package playback

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/models"
)

type markerUpdateSessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

// MarkerUpdateNotifier publishes live marker updates to active playback sessions.
type MarkerUpdateNotifier struct {
	sessions markerUpdateSessionLookup
	hub      *RealtimeHub
}

func NewMarkerUpdateNotifier(sessions markerUpdateSessionLookup, hub *RealtimeHub) *MarkerUpdateNotifier {
	if sessions == nil || hub == nil {
		return nil
	}
	return &MarkerUpdateNotifier{
		sessions: sessions,
		hub:      hub,
	}
}

func (n *MarkerUpdateNotifier) MarkersUpdated(ctx context.Context, file *models.MediaFile) {
	if n == nil || file == nil || file.ID <= 0 {
		return
	}

	rangePayload := func(start, end *float64) *TimeRangePayload {
		if start == nil || end == nil {
			return nil
		}
		return &TimeRangePayload{Start: *start, End: *end}
	}
	intro := rangePayload(file.IntroStart, file.IntroEnd)
	credits := rangePayload(file.CreditsStart, file.CreditsEnd)
	recap := rangePayload(file.RecapStart, file.RecapEnd)
	preview := rangePayload(file.PreviewStart, file.PreviewEnd)

	for _, session := range n.sessions.GetSessionsByMediaFileID(file.ID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		event, err := NewMarkersUpdatedEvent(session.ID, file.ID, intro, credits, recap, preview)
		if err != nil {
			slog.WarnContext(ctx,
				"failed to encode markers updated realtime event", "component", "playback",
				"session_id",
				session.ID,
				"file_id",
				file.ID,
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
				file.ID,
				"error",
				err,
			)
		}
	}
}

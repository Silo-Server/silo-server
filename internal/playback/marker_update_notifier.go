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
//
// The four ranges ride the snapshot rather than being derived when it is
// delivered, because a replica that republishes an update has only this struct
// and provenance is not in the marker segments: with "show marker source" on, a
// replica that dropped it would blank the line a viewer asked for.
type markerUpdateSnapshot struct {
	SourceID string                 `json:"source_id"`
	FileID   int                    `json:"file_id"`
	Intro    *TimeRangePayload      `json:"intro"`
	Credits  *TimeRangePayload      `json:"credits"`
	Recap    *TimeRangePayload      `json:"recap"`
	Preview  *TimeRangePayload      `json:"preview"`
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
	snapshot.Intro, snapshot.Credits, snapshot.Recap, snapshot.Preview = markerUpdateRanges(file, snapshot.Segments)
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

// markerUpdateRanges reports the file's first occurrence of each marker kind as
// the event sends it, each with the provenance the row records for that kind.
//
// The ranges come from the same effective segment list the event carries, so
// the two cannot disagree about which occurrence is the kind's range. Provenance
// is per file and kind rather than per occurrence — one row holds one origin for
// the intro — so every occurrence of a kind reports the same answer.
func markerUpdateRanges(file *models.MediaFile, segments []models.MarkerSegment) (*TimeRangePayload, *TimeRangePayload, *TimeRangePayload, *TimeRangePayload) {
	first := func(kind string) *TimeRangePayload {
		for _, segment := range segments {
			if segment.Kind != kind {
				continue
			}
			source, provider, algorithm, confidence := markerProvenanceOf(file, kind)
			return &TimeRangePayload{
				Start:      segment.StartSeconds,
				End:        segment.EndSeconds,
				Source:     source,
				Provider:   provider,
				Confidence: confidence,
				Algorithm:  algorithm,
			}
		}
		return nil
	}
	return first(models.MarkerSegmentIntro), first(models.MarkerSegmentCredits),
		first(models.MarkerSegmentRecap), first(models.MarkerSegmentPreview)
}

// markerProvenanceOf reads one kind's provenance off the file row.
//
// Confidence comes back as a pointer because the column is nullable: a range
// nobody rated records none, and a client handed that as the number 0 would
// print a measurement of nothing.
func markerProvenanceOf(file *models.MediaFile, kind string) (source, provider, algorithm string, confidence *float64) {
	if file == nil {
		return "", "", "", nil
	}
	read := func(source, provider, algorithm *string, confidence *float64) (string, string, string, *float64) {
		return stringOrEmpty(source), stringOrEmpty(provider), stringOrEmpty(algorithm), confidence
	}
	switch kind {
	case models.MarkerSegmentIntro:
		return read(file.IntroMarkersSource, file.IntroMarkersProvider, file.IntroMarkersAlgorithm, file.IntroMarkersConfidence)
	case models.MarkerSegmentCredits:
		return read(file.CreditsMarkersSource, file.CreditsMarkersProvider, file.CreditsMarkersAlgorithm, file.CreditsMarkersConfidence)
	case models.MarkerSegmentRecap:
		return read(file.RecapMarkersSource, file.RecapMarkersProvider, file.RecapMarkersAlgorithm, file.RecapMarkersConfidence)
	case models.MarkerSegmentPreview:
		return read(file.PreviewMarkersSource, file.PreviewMarkersProvider, file.PreviewMarkersAlgorithm, file.PreviewMarkersConfidence)
	default:
		return "", "", "", nil
	}
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (n *MarkerUpdateNotifier) dispatch(ctx context.Context, snapshot markerUpdateSnapshot) {
	for _, session := range n.sessions.GetSessionsByMediaFileID(snapshot.FileID) {
		if ctx.Err() != nil {
			return
		}
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		event, err := NewMarkersUpdatedEvent(session.ID, snapshot.FileID, snapshot.Intro, snapshot.Credits, snapshot.Recap, snapshot.Preview, snapshot.Segments...)
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

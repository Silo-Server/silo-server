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

	// watches tracks the session and file of each reconnect snapshot in
	// flight. dispatch bumps a watch immediately before it writes an update to
	// that session, so a snapshot read from the database is dropped exactly
	// when a newer update for the same file was sent to the same session while
	// the row was being read. Only in-flight snapshots are tracked, so memory
	// stays bounded.
	watchMu sync.Mutex
	watches map[markerWatchKey]*markerSnapshotWatch
}

type markerWatchKey struct {
	fileID    int
	sessionID string
}

type markerSnapshotWatch struct {
	refs  int
	epoch uint64
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

func (n *MarkerUpdateNotifier) watch(key markerWatchKey) *markerSnapshotWatch {
	n.watchMu.Lock()
	defer n.watchMu.Unlock()
	if n.watches == nil {
		n.watches = make(map[markerWatchKey]*markerSnapshotWatch)
	}
	watch := n.watches[key]
	if watch == nil {
		watch = &markerSnapshotWatch{}
		n.watches[key] = watch
	}
	watch.refs++
	return watch
}

func (n *MarkerUpdateNotifier) unwatch(key markerWatchKey, watch *markerSnapshotWatch) {
	n.watchMu.Lock()
	defer n.watchMu.Unlock()
	watch.refs--
	if watch.refs == 0 && n.watches[key] == watch {
		delete(n.watches, key)
	}
}

// markDelivering bumps the watch for a snapshot that the update about to be
// written to sessionID supersedes.
func (n *MarkerUpdateNotifier) markDelivering(fileID int, sessionID string) {
	n.watchMu.Lock()
	defer n.watchMu.Unlock()
	if watch := n.watches[markerWatchKey{fileID: fileID, sessionID: sessionID}]; watch != nil {
		watch.epoch++
	}
}

func (n *MarkerUpdateNotifier) watchEpoch(watch *markerSnapshotWatch) uint64 {
	n.watchMu.Lock()
	defer n.watchMu.Unlock()
	return watch.epoch
}

func markersUpdatedEventForSegments(sessionID string, fileID int, segments []models.MarkerSegment) (EventEnvelope, error) {
	firstRange := func(kind string) *TimeRangePayload {
		for _, segment := range segments {
			if segment.Kind == kind {
				return &TimeRangePayload{Start: segment.StartSeconds, End: segment.EndSeconds}
			}
		}
		return nil
	}
	return NewMarkersUpdatedEvent(sessionID, fileID, firstRange("intro"), firstRange("credits"), firstRange("recap"), firstRange("preview"), segments...)
}

// SendSnapshot sends the stored markers for fileID to one realtime
// registration, so a player that reconnects learns about updates it missed
// while it was disconnected. It sends nothing when the file has no markers,
// since an all-empty event would clear what the player already shows. load
// runs outside every lock. If a newer update for the same file is written to
// this session while the row is being read, the snapshot is dropped, because
// the session already has the newer markers.
func (n *MarkerUpdateNotifier) SendSnapshot(
	ctx context.Context,
	registration *RealtimeRegistration,
	fileID int,
	load func(context.Context, int) (*models.MediaFile, error),
) (bool, error) {
	if n == nil || registration == nil || registration.sessionID == "" || fileID <= 0 || load == nil {
		return false, nil
	}
	key := markerWatchKey{fileID: fileID, sessionID: registration.sessionID}
	watch := n.watch(key)
	defer n.unwatch(key, watch)
	start := n.watchEpoch(watch)
	file, err := load(ctx, fileID)
	if err != nil || file == nil {
		return false, err
	}
	segments := models.EffectiveMarkerSegments(file)
	if len(segments) == 0 {
		return false, nil
	}
	event, err := markersUpdatedEventForSegments(registration.sessionID, fileID, segments)
	if err != nil {
		return false, err
	}
	sent, err := n.hub.SendRegisteredIf(registration, event, func() bool { return n.watchEpoch(watch) == start })
	if errors.Is(err, ErrRealtimeConnectionNotFound) {
		return false, nil
	}
	return sent, err
}

func (n *MarkerUpdateNotifier) dispatch(ctx context.Context, snapshot markerUpdateSnapshot) {
	for _, session := range n.sessions.GetSessionsByMediaFileID(snapshot.FileID) {
		if ctx.Err() != nil {
			return
		}
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		event, err := markersUpdatedEventForSegments(session.ID, snapshot.FileID, snapshot.Segments)
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
		// Bump before the write: a snapshot whose send check runs after this
		// point is dropped, and one that already wrote holds the lane lock, so
		// this newer update is written after it.
		n.markDelivering(snapshot.FileID, session.ID)
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

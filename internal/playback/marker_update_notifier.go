package playback

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/Silo-Server/silo-server/internal/models"
)

type markerUpdateSessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

// MarkerUpdateNotifier publishes live marker updates to active playback sessions.
type MarkerUpdateNotifier struct {
	sessions markerUpdateSessionLookup
	hub      *RealtimeHub
	// A small fixed lock set serializes each file's persisted snapshot read
	// with provider-update delivery. This prevents an older partial snapshot
	// from landing after a newer markers_updated event without growing a lock
	// map for every file in a large library.
	fileLocks [64]sync.Mutex
}

// MarkerSnapshotFileLoader loads the persisted media-file marker row used for
// a reconnect snapshot.
type MarkerSnapshotFileLoader interface {
	GetByID(ctx context.Context, fileID int) (*models.MediaFile, error)
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
	lock := n.fileLock(file.ID)
	lock.Lock()
	defer lock.Unlock()

	for _, session := range n.sessions.GetSessionsByMediaFileID(file.ID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		n.sendSessionSnapshotLocked(ctx, session.ID, file)
	}
}

// SendSessionSnapshot sends the current persisted marker ranges to one live
// playback session. Calling it after the client's hello closes the race where
// lazy marker discovery finishes before the websocket is control-ready.
func (n *MarkerUpdateNotifier) SendSessionSnapshot(ctx context.Context, sessionID string, file *models.MediaFile) {
	if n == nil || n.hub == nil || sessionID == "" || file == nil || file.ID <= 0 {
		return
	}
	lock := n.fileLock(file.ID)
	lock.Lock()
	defer lock.Unlock()
	n.sendSessionSnapshotLocked(ctx, sessionID, file)
}

// SendSessionSnapshotFromLoader holds the same per-file ordering lock across
// the database read and websocket send used by MarkersUpdated. A concurrent
// provider write therefore lands either wholly before this fresh read or
// wholly after this snapshot, never as a newer event followed by stale data.
func (n *MarkerUpdateNotifier) SendSessionSnapshotFromLoader(
	ctx context.Context,
	sessionID string,
	fileID int,
	loader MarkerSnapshotFileLoader,
) error {
	if n == nil || n.hub == nil || sessionID == "" || fileID <= 0 || loader == nil {
		return nil
	}
	lock := n.fileLock(fileID)
	lock.Lock()
	defer lock.Unlock()
	file, err := loader.GetByID(ctx, fileID)
	if err != nil || file == nil {
		return err
	}
	n.sendSessionSnapshotLocked(ctx, sessionID, file)
	return nil
}

func (n *MarkerUpdateNotifier) fileLock(fileID int) *sync.Mutex {
	return &n.fileLocks[uint(fileID)%uint(len(n.fileLocks))]
}

func (n *MarkerUpdateNotifier) sendSessionSnapshotLocked(ctx context.Context, sessionID string, file *models.MediaFile) {
	rangePayload := func(start, end *float64) *TimeRangePayload {
		if start == nil || end == nil {
			return nil
		}
		return &TimeRangePayload{Start: *start, End: *end}
	}
	event, err := NewMarkersUpdatedEvent(
		sessionID,
		file.ID,
		rangePayload(file.IntroStart, file.IntroEnd),
		rangePayload(file.CreditsStart, file.CreditsEnd),
		rangePayload(file.RecapStart, file.RecapEnd),
		rangePayload(file.PreviewStart, file.PreviewEnd),
	)
	if err != nil {
		slog.WarnContext(ctx,
			"failed to encode markers updated realtime event", "component", "playback",
			"session_id", sessionID,
			"file_id", file.ID,
			"error", err,
		)
		return
	}
	if err := n.hub.Send(sessionID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
		slog.WarnContext(ctx,
			"failed to deliver markers updated realtime event", "component", "playback",
			"session_id", sessionID,
			"file_id", file.ID,
			"error", err,
		)
	}
}

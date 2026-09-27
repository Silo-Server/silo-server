package watchtogether

import (
	"context"
	"time"
)

const (
	// itemEndToleranceSeconds is how close to the end of the file the room's
	// position must be for the item to count as finished. A player stops at
	// its own idea of the end, which can sit a little short of the stored
	// duration, and that duration is whole seconds.
	itemEndToleranceSeconds = 2.0
	// itemEndRefreshInterval is how often the host's file is looked up again.
	// A replan can move the host's session to another file of the same title.
	itemEndRefreshInterval = 30 * time.Second
	// itemEndLookupTimeout bounds the lookups, which run outside the room
	// operation's deadline. A stalled lookup must not hold up the reconciler
	// that renews every local socket's lease; the next tick tries again.
	itemEndLookupTimeout = roomReconcileInterval
)

// itemEnd is the duration of the file a room is playing, for the selection
// and pinned file it was resolved for.
type itemEnd struct {
	revision    int64
	fileID      int
	seconds     float64
	hostSession string
	checkedAt   time.Time
}

// itemEndSourceLocked names what the room's end depends on: the selection,
// its pinned file if any, and otherwise the host's attached session, since
// the room follows the host.
func itemEndSourceLocked(live *liveRoom) (revision int64, fileID int, hostSession string) {
	if live.room.SelectedFileID != nil {
		fileID = *live.room.SelectedFileID
	}
	if host := live.members[buildMemberKey(live.room.HostUserID, live.room.HostProfileID)]; host != nil {
		hostSession = host.sessionID
	}
	return live.room.SelectionRevision, fileID, hostSession
}

// refreshItemEnd looks up the playing file's duration. It runs before the
// room operation, so catalog access stays outside the row lock. A failed
// lookup keeps the last known duration: a host who leaves the player stops
// their session, and the room still has to finish.
func (s *Service) refreshItemEnd(ctx context.Context, roomID string) {
	s.mu.Lock()
	live := s.rooms[roomID]
	if live == nil || live.room.Phase != RoomPhasePlaying || s.files == nil {
		s.mu.Unlock()
		return
	}
	revision, fileID, hostSession := itemEndSourceLocked(live)
	cached := live.itemEnd
	now := s.now()
	known := cached.revision == revision && cached.fileID == fileID && cached.seconds > 0
	if known && (fileID != 0 || hostSession == "" ||
		(hostSession == cached.hostSession && now.Sub(cached.checkedAt) < itemEndRefreshInterval)) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	lookupCtx, cancel := context.WithTimeout(ctx, itemEndLookupTimeout)
	seconds := s.lookupItemEnd(lookupCtx, fileID, hostSession)
	cancel()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rooms[roomID] != live {
		return
	}
	if seconds <= 0 {
		if known {
			live.itemEnd.checkedAt = now
		}
		return
	}
	live.itemEnd = itemEnd{revision: revision, fileID: fileID, seconds: seconds, hostSession: hostSession, checkedAt: now}
}

func (s *Service) lookupItemEnd(ctx context.Context, fileID int, hostSession string) float64 {
	if fileID == 0 {
		if hostSession == "" {
			return 0
		}
		session, err := s.lookupSession(ctx, hostSession)
		if err != nil || session == nil {
			return 0
		}
		fileID = session.MediaFileID
	}
	file, err := s.files.GetByID(ctx, fileID)
	if err != nil || file == nil {
		return 0
	}
	return float64(file.Duration)
}

// itemFinishedLocked reports whether the room's position has reached the end
// of the item it is playing. A host who plays to the end reports a paused
// position there; a host who leaves the player lets the room's clock run past
// it. Either way the room is done with the item.
func (s *Service) itemFinishedLocked(live *liveRoom) bool {
	if live.room.Phase != RoomPhasePlaying {
		return false
	}
	revision, fileID, _ := itemEndSourceLocked(live)
	end := live.itemEnd
	if end.revision != revision || end.fileID != fileID || end.seconds <= itemEndToleranceSeconds {
		return false
	}
	return expectedPosition(live.room, s.now()) >= end.seconds-itemEndToleranceSeconds
}

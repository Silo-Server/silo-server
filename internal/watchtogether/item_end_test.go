package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type filesByID map[int]*models.MediaFile

func (f filesByID) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	file := f[id]
	if file == nil {
		return nil, errors.New("missing file")
	}
	cp := *file
	return &cp, nil
}

type itemEndRoom struct {
	s        *Service
	repo     *stubRepo
	sessions *stubSessions
	files    filesByID
	host     *recordingConn
	now      time.Time
}

// newItemEndRoom plays file 42 of the given duration with the host attached,
// anchored at the given position.
func newItemEndRoom(t *testing.T, duration int, anchor float64, paused bool) *itemEndRoom {
	t.Helper()
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	room := baseRoom(now)
	room.AnchorPositionSeconds, room.IsPaused, room.AnchorUpdatedAt = anchor, paused, now
	if paused {
		room.PlaybackState = RoomPlaybackStatePaused
	}
	f := &itemEndRoom{
		repo:     &stubRepo{room: room},
		sessions: &stubSessions{session: &playback.Session{ID: "host-session", UserID: 7, ProfileID: "host", MediaFileID: 42}},
		files:    filesByID{42: {ID: 42, ContentID: "movie-1", Duration: duration}},
		host:     new(recordingConn),
		now:      now,
	}
	f.s = newServiceForTest(now, f.repo, f.sessions, nil, nil)
	f.s.files = f.files
	f.s.now = func() time.Time { return f.now }
	t.Cleanup(f.s.Close)
	f.s.rooms[room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID: 7, profileID: "host", sessionID: "host-session", connection: f.host, isReady: true,
	}
	return f
}

func (f *itemEndRoom) reconcile(t *testing.T) {
	t.Helper()
	if err := f.s.reconcileRoom(t.Context(), f.repo.room.ID); err != nil {
		t.Fatal(err)
	}
}

func (f *itemEndRoom) phase() RoomPhase { return f.repo.room.Phase }

func TestHostPausedAtTheEndReturnsTheRoomToTheLobby(t *testing.T) {
	f := newItemEndRoom(t, 100, 99.2, true)
	f.reconcile(t)
	if f.phase() != RoomPhaseLobby || f.repo.room.SelectedContentID == nil {
		t.Fatalf("finished room = %+v, want the lobby with the item staged", f.repo.room)
	}
	var sawLobby bool
	for _, payload := range f.host.payloads {
		if snapshot, ok := payload["room"].(Snapshot); ok && snapshot.Phase == RoomPhaseLobby {
			sawLobby = true
		}
	}
	if !sawLobby {
		t.Fatalf("host was not sent the lobby: %+v", f.host.payloads)
	}
}

func TestPlayingRoomReturnsToTheLobbyOnceItsClockReachesTheEnd(t *testing.T) {
	f := newItemEndRoom(t, 100, 90, false)
	f.now = f.now.Add(5 * time.Second)
	f.reconcile(t)
	if f.phase() != RoomPhasePlaying {
		t.Fatalf("room at 95s of 100s left playing: %s", f.phase())
	}
	f.now = f.now.Add(4 * time.Second)
	f.reconcile(t)
	if f.phase() != RoomPhaseLobby {
		t.Fatalf("room at 99s of 100s = %s, want lobby", f.phase())
	}
}

func TestRoomFinishesAfterTheHostLeavesThePlayer(t *testing.T) {
	f := newItemEndRoom(t, 100, 50, false)
	f.reconcile(t)
	// Leaving the player stops the host's session; the room's clock runs on.
	f.sessions.session = nil
	f.now = f.now.Add(49 * time.Second)
	f.reconcile(t)
	if f.phase() != RoomPhaseLobby {
		t.Fatalf("room at 99s of 100s after the host left = %s, want lobby", f.phase())
	}
}

func TestPinnedFileFinishesWithoutTheHostSession(t *testing.T) {
	f := newItemEndRoom(t, 100, 99, true)
	f.files[42] = nil
	f.files[7] = &models.MediaFile{ID: 7, ContentID: "movie-1", Duration: 100}
	pinned := 7
	f.repo.room.SelectedFileID = &pinned
	f.s.rooms[f.repo.room.ID].room.SelectedFileID = &pinned
	f.sessions.session = nil
	f.reconcile(t)
	if f.phase() != RoomPhaseLobby {
		t.Fatalf("pinned file at its end = %s, want lobby", f.phase())
	}
}

func TestReplannedHostFileIsLookedUpAgain(t *testing.T) {
	f := newItemEndRoom(t, 100, 30, false)
	f.reconcile(t)
	// A replan moves the host's session to a shorter file of the same title.
	f.files[43] = &models.MediaFile{ID: 43, ContentID: "movie-1", Duration: 60}
	f.sessions.session.MediaFileID = 43
	f.now = f.now.Add(itemEndRefreshInterval + time.Second)
	f.reconcile(t)
	if f.phase() != RoomPhaseLobby {
		t.Fatalf("room at 61s of the replanned 60s file = %s, want lobby", f.phase())
	}
}

func TestRoomPausedBeforeTheEndKeepsPlaying(t *testing.T) {
	f := newItemEndRoom(t, 100, 97.5, true)
	f.now = f.now.Add(time.Hour)
	f.reconcile(t)
	if f.phase() != RoomPhasePlaying {
		t.Fatalf("room paused 2.5s before the end = %s, want playing", f.phase())
	}
}

func TestRoomWithoutAKnownDurationKeepsPlaying(t *testing.T) {
	f := newItemEndRoom(t, 0, 5000, false)
	f.reconcile(t)
	if f.phase() != RoomPhasePlaying {
		t.Fatalf("room without a duration = %s, want playing", f.phase())
	}
}

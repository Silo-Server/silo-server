package watchtogether

import (
	"testing"
	"time"
)

func TestPausedRoomIgnoresLateBuffering(t *testing.T) {
	now := time.Now().UTC()
	repo := &stubRepo{room: baseRoom(now)}
	s := newServiceForTest(now, repo, nil, nil, nil)
	t.Cleanup(s.Close)
	conn := new(recordingConn)
	live := s.rooms[repo.room.ID]
	live.members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", sessionID: "session", connection: conn, isReady: true}
	reg := registrationFor(repo.room.ID, 7, "host", conn)
	paused, err := s.HandleTransportRequestForConnection(t.Context(), reg, 7, "host", TransportRequest{Action: TransportActionPause, PositionSeconds: new(120.0)})
	if err != nil {
		t.Fatal(err)
	}
	frames := len(conn.payloads)
	snapshot, err := s.HandleBufferingForConnection(t.Context(), reg, 7, "host", StateReport{SessionID: "session", PositionSeconds: 119, IsPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlaybackState != RoomPlaybackStatePaused || snapshot.Generation != paused.Generation || snapshot.AnchorPositionSeconds != 120 || len(conn.payloads) != frames {
		t.Fatalf("late buffering reopened paused room: %+v", snapshot)
	}
}

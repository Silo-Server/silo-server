package watchtogether

import (
	"errors"
	"testing"
	"time"
)

func TestTransportActionConflictCannotBeAcknowledgedAsApplied(t *testing.T) {
	for _, reportConflict := range []bool{false, true} {
		now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
		repo := &stubRepo{room: baseRoom(now)}
		service := newServiceForTest(now, repo, nil, nil, nil)
		t.Cleanup(service.Close)
		// A different writer has already committed the authoritative winner.
		repo.room.Generation = 5
		conn := new(recordingConn)
		service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
			userID: 7, profileID: "host", sessionID: "session", connection: conn,
		}
		reg := registrationFor(repo.room.ID, 7, "host", conn)
		snapshot, err := service.HandleTransportRequestForConnection(t.Context(), reg, 7, "host", TransportRequest{
			Action: TransportActionPause, ReportConflict: reportConflict,
		})
		if reportConflict && !errors.Is(err, ErrRoomStateConflict) {
			t.Fatalf("failed action could be acknowledged as applied: %v", err)
		}
		if !reportConflict && err != nil {
			t.Fatalf("legacy conflict behavior changed: %v", err)
		}
		if snapshot.Generation != 5 || len(conn.payloads) != 0 {
			t.Fatalf("conflict must retain the winner without issuing commands: %+v, %d frames", snapshot, len(conn.payloads))
		}
	}
}

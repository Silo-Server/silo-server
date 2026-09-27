package watchtogether

import (
	"errors"
	"testing"
	"time"
)

func TestHostCloseAuthorityAndDispatch(t *testing.T) {
	now := time.Now()
	repo := &stubRepo{room: baseRoom(now)}
	svc := newServiceForTest(now, repo, nil, nil, nil)
	conn := new(recordingConn)
	svc.rooms["room-1"].members["guest"] = &memberState{userID: 8, profileID: "guest", connection: conn}
	for _, who := range []struct {
		user    int
		profile string
	}{{7, "guest"}, {8, "host"}} {
		if err := svc.CloseRoom(t.Context(), "room-1", who.user, who.profile); !errors.Is(err, ErrRoomForbidden) {
			t.Fatalf("authority: %v", err)
		}
		if repo.room.Phase == RoomPhaseEnded {
			t.Fatal("unauthorized close persisted")
		}
	}
	if err := svc.CloseRoom(t.Context(), "room-1", 7, "host"); err != nil {
		t.Fatal(err)
	}
	if repo.room.Phase != RoomPhaseEnded || repo.room.ClosedAt == nil || svc.rooms["room-1"] != nil {
		t.Fatal("close state incomplete")
	}
	if len(conn.payloads) != 1 || conn.payloads[0]["type"] != "room_closed" || conn.payloads[0]["reason"] != "host_left" {
		t.Fatal("close dispatch changed")
	}
	if err := svc.CloseRoom(t.Context(), "room-1", 7, "host"); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("repeat: %v", err)
	}
}

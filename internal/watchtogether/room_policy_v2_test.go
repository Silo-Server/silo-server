package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"
)

type competingPolicyRepo struct {
	*stubRepo
	calls int
}

func (r *competingPolicyRepo) UpdatePolicy(context.Context, string, GuestControlPolicy, int64, int64) (*Room, error) {
	r.calls++
	r.room.Generation += 2
	r.room.GuestControlPolicy = GuestControlPolicyHostOnly
	return nil, ErrRoomStateConflict
}
func TestPolicyHostAuthorityAndCompetingWinner(t *testing.T) {
	now := time.Now()
	repo := &stubRepo{room: baseRoom(now)}
	svc := newServiceForTest(now, repo, nil, nil, nil)
	for _, who := range []struct {
		user    int
		profile string
	}{{7, "guest"}, {8, "host"}} {
		if _, err := svc.UpdatePolicy(t.Context(), "room-1", who.user, who.profile, GuestControlPolicyGuestPlayPause); !errors.Is(err, ErrRoomForbidden) {
			t.Fatalf("authority: %v", err)
		}
	}
	conn := new(recordingConn)
	svc.rooms["room-1"].members["guest"] = &memberState{userID: 8, profileID: "guest", connection: conn}
	got, err := svc.UpdatePolicy(t.Context(), "room-1", 7, "host", GuestControlPolicyGuestPlayPause)
	if err != nil || got.GuestControlPolicy != GuestControlPolicyGuestPlayPause || len(conn.payloads) != 1 {
		t.Fatalf("policy/dispatch: %v", err)
	}
	competing := &competingPolicyRepo{stubRepo: repo}
	svc.repo = competing
	before := len(conn.payloads)
	got, err = svc.UpdatePolicy(t.Context(), "room-1", 7, "host", GuestControlPolicyGuestPlayPause)
	if err != nil || got.GuestControlPolicy != GuestControlPolicyHostOnly || competing.calls != 1 || len(conn.payloads) != before {
		t.Fatal("conflict did not retain authoritative winner without replay/broadcast")
	}
}

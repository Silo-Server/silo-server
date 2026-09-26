package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"
)

type replacementConn struct {
	recordingConn
	closed, replaced bool
}

func (c *replacementConn) Close() error { c.closed = true; return nil }
func (c *replacementConn) CloseReplaced() error {
	c.replaced = true
	return c.Close()
}

func TestConnectionReplacementPreservesWinner(t *testing.T) {
	now := time.Now()
	repo := &stubRepo{room: baseRoom(now)}
	s := newServiceForTest(now, repo, nil, nil, nil)
	t.Cleanup(s.Close)
	old, winner := new(replacementConn), new(replacementConn)
	oldReg, _, err := s.Connect(t.Context(), repo.room.ID, 7, "host", old)
	if err != nil {
		t.Fatal(err)
	}
	winnerReg, _, err := s.Connect(t.Context(), repo.room.ID, 7, "host", winner)
	if err != nil {
		t.Fatal(err)
	}
	if !old.replaced || !old.closed {
		t.Fatal("displaced connection did not receive terminal replacement")
	}
	s.Disconnect(oldReg, true)
	s.closeIfHostStillDisconnected(repo.room.ID, 7, "host")
	if winner.closed {
		t.Fatal("stale disconnect closed the winner")
	}
	if err := s.HandlePingForConnection(t.Context(), winnerReg, 7, "host", 10); err != nil {
		t.Fatal(err)
	}
	if room, err := s.GetRoom(t.Context(), repo.room.ID); err != nil || room.Phase == RoomPhaseEnded {
		t.Fatalf("replacement ended room: %+v %v", room, err)
	}
}

func TestCrossNodeConnectionReplacementPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	old, winner := new(replacementConn), new(replacementConn)
	oldReg, _, err := f.host.Connect(t.Context(), f.roomID, 7, "host", old)
	if err != nil {
		t.Fatal(err)
	}
	winnerReg, _, err := f.guest.Connect(t.Context(), f.roomID, 7, "host", winner)
	if err != nil {
		t.Fatal(err)
	}
	if old.closed {
		t.Fatal("other node closed socket before reconciliation")
	}
	if err := f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	if !old.replaced || !old.closed {
		t.Fatal("cross-node replacement omitted terminal reason")
	}
	f.host.Disconnect(oldReg, true)
	f.host.closeIfHostStillDisconnected(f.roomID, 7, "host")
	if winner.closed {
		t.Fatal("stale callback closed cross-node winner")
	}
	if err := f.guest.HandlePingForConnection(t.Context(), winnerReg, 7, "host", 10); err != nil {
		t.Fatal(err)
	}
	if room, err := f.repo.GetRoomByID(t.Context(), f.roomID); err != nil || room.Phase == RoomPhaseEnded {
		t.Fatalf("replacement ended room: %+v %v", room, err)
	}
}

func TestReplacementIsNotSentBeforeCommitPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	old := new(replacementConn)
	if _, _, err := f.host.Connect(t.Context(), f.roomID, 7, "host", old); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("rollback replacement")
	_, err := withRoomOperation(t.Context(), f.host, f.roomID, func(ctx context.Context) (Snapshot, error) {
		if _, _, err := f.host.connect(ctx, f.roomID, 7, "host", new(replacementConn), "Host"); err != nil {
			t.Fatal(err)
		}
		if old.closed {
			t.Fatal("replacement escaped transaction")
		}
		return Snapshot{}, failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if old.closed || old.replaced {
		t.Fatal("rolled back replacement displaced live connection")
	}
}

func TestRuntimeDisconnectIsNotConnectionReplacement(t *testing.T) {
	for _, cause := range []string{"missing", "disconnected", "expired", "room_ended"} {
		t.Run(cause, func(t *testing.T) {
			now := time.Now()
			room := baseRoom(now)
			s := newServiceForTest(now, &stubRepo{room: room}, nil, nil, nil)
			t.Cleanup(s.Close)
			old := new(replacementConn)
			key := buildMemberKey(7, "host")
			live := &liveRoom{room: room, members: map[string]*memberState{key: {userID: 7, profileID: "host", connectionID: "old", connection: old}}}
			state := roomRuntime{Members: map[string]runtimeMember{}}
			if cause != "missing" {
				state.Members[key] = runtimeMember{UserID: 7, ProfileID: "host", ConnectionID: "new", Connected: cause != "disconnected", LeaseUntil: now.Add(time.Minute)}
			}
			if cause == "expired" {
				stored := state.Members[key]
				stored.LeaseUntil = now.Add(-time.Second)
				state.Members[key] = stored
			}
			if cause == "room_ended" {
				room.Phase = RoomPhaseEnded
			}
			s.adoptRuntimeLocked(t.Context(), live, room, state)
			if !old.closed || old.replaced {
				t.Fatalf("%s treated as replacement: %+v", cause, old)
			}
		})
	}
}

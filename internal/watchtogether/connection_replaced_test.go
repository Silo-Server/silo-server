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
		if _, _, err := f.host.connect(ctx, f.roomID, 7, "host", new(replacementConn), "Host", false); err != nil {
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

// TestOtherAppDoesNotTakeAConnectedSeat: a member is its account and
// profile, so a session another app opened, connecting while the person's
// own device holds that seat, would close the device's socket and inherit its
// attached stream. It is refused and the device stays connected; with the
// seat free it connects, and the person's device connecting later still
// replaces it. An ordinary connection replaces as before
// (TestConnectionReplacementPreservesWinner).
func TestOtherAppDoesNotTakeAConnectedSeat(t *testing.T) {
	now := time.Now()
	repo := &stubRepo{room: baseRoom(now)}
	s := newServiceForTest(now, repo, nil, nil, nil)
	t.Cleanup(s.Close)
	device, app := new(replacementConn), new(replacementConn)
	if _, _, err := s.Connect(t.Context(), repo.room.ID, 8, "guest", device); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ConnectOtherApp(t.Context(), repo.room.ID, 8, "guest", app); !errors.Is(err, ErrMemberAlreadyConnected) {
		t.Fatalf("another app taking a connected seat: err = %v", err)
	}
	if device.closed {
		t.Fatal("the refused app closed the device's connection")
	}

	seatless, later := new(replacementConn), new(replacementConn)
	if _, _, err := s.ConnectOtherApp(t.Context(), repo.room.ID, 9, "free", seatless); err != nil {
		t.Fatalf("another app on a free seat: err = %v", err)
	}
	if _, _, err := s.Connect(t.Context(), repo.room.ID, 9, "free", later); err != nil || !seatless.replaced {
		t.Fatalf("the person's device did not replace the app: err = %v, replaced %v", err, seatless.replaced)
	}
}

// TestOtherAppDoesNotTakeASeatOnAnotherServerPG: the seat check reads the
// room's stored runtime under its row lock, so a device connected to another
// API server holds its seat against a session another app opened here.
func TestOtherAppDoesNotTakeASeatOnAnotherServerPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	app := new(replacementConn)
	if _, _, err := f.guest.ConnectOtherApp(t.Context(), f.roomID, 7, "host", app); !errors.Is(err, ErrMemberAlreadyConnected) {
		t.Fatalf("another app taking a seat held on another server: err = %v", err)
	}
	if err := f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	if err := f.host.HandlePingForConnection(t.Context(), f.hostReg, 7, "host", 10); err != nil {
		t.Fatalf("the device lost its seat on the other server: %v", err)
	}
}

// TestOtherAppStartsWithNoAttachedStream: a member is its account and
// profile, so a stream the person's own device attached stays on the member
// after that device leaves its seat, and a session another app opened taking
// the free seat would be shown that device's playback session id in its
// snapshot. Such a connection starts with no attached stream; an ordinary
// reconnection keeps it. Catches another device's id reaching such a session
// through the room snapshot.
func TestOtherAppStartsWithNoAttachedStream(t *testing.T) {
	for _, tc := range []struct {
		name     string
		otherApp bool
		want     string
	}{{"another app", true, ""}, {"the person's own device", false, "phone-stream"}} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			repo := &stubRepo{room: baseRoom(now)}
			s := newServiceForTest(now, repo, nil, nil, nil)
			t.Cleanup(s.Close)
			if _, _, err := s.Connect(t.Context(), repo.room.ID, 8, "guest", new(replacementConn)); err != nil {
				t.Fatal(err)
			}
			// The device attached a stream, then its seat came free.
			s.mu.Lock()
			member := s.rooms[repo.room.ID].members[buildMemberKey(8, "guest")]
			member.sessionID, member.connection, member.remoteConnected = "phone-stream", nil, false
			s.mu.Unlock()
			connect := s.Connect
			if tc.otherApp {
				connect = s.ConnectOtherApp
			}
			_, snapshot, err := connect(t.Context(), repo.room.ID, 8, "guest", new(replacementConn))
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.AttachedSessionID != tc.want {
				t.Fatalf("attached session %q, want %q", snapshot.AttachedSessionID, tc.want)
			}
		})
	}
}

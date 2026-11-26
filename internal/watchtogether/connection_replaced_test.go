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

// TestOtherAppSnapshotsNameNoRefusedPower: a member is its account and
// profile, so a session another app opened connected as the host's profile,
// or as a guest while the room lets guests play and pause, would be told it
// may manage the room (with the host's invite link) or control transport,
// which the socket and the gate refuse it. Its snapshots, the one connect
// returns and the ones broadcast to it, name none of those powers; the
// role still names the profile's seat. The person's own connection keeps
// them. Catches such a session being offered controls it cannot use.
func TestOtherAppSnapshotsNameNoRefusedPower(t *testing.T) {
	for _, tc := range []struct {
		name          string
		otherApp      bool
		manage, steer bool
	}{{"another app", true, false, false}, {"the person's own device", false, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			room := baseRoom(now)
			room.GuestControlPolicy = GuestControlPolicyGuestPlayPause
			repo := &stubRepo{room: room}
			s := newServiceForTest(now, repo, nil, nil, nil)
			t.Cleanup(s.Close)
			connect := s.Connect
			if tc.otherApp {
				connect = s.ConnectOtherApp
			}
			_, snapshot, err := connect(t.Context(), repo.room.ID, 7, "host", new(replacementConn))
			if err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			live := s.rooms[repo.room.ID]
			broadcast := personalizeSnapshot(s.buildSnapshotLocked(live, 0, ""), live.room, live.members[buildMemberKey(7, "host")])
			s.mu.Unlock()
			for name, got := range map[string]Snapshot{"connect": snapshot, "broadcast": broadcast} {
				if got.SelfCanManageRoom != tc.manage || got.SelfCanControlTransport != tc.steer || (got.InvitePath != "") != tc.manage {
					t.Fatalf("%s: manage %v, control %v, invite %q", name, got.SelfCanManageRoom, got.SelfCanControlTransport, got.InvitePath)
				}
				if got.SelfRole != MemberRoleHost {
					t.Fatalf("%s: role %q, want the seat's host", name, got.SelfRole)
				}
			}
		})
	}
}

// TestOtherAppMarkFollowsTheConnection: the mark that strips the room powers
// belongs to the app's connection, never to the seat it took. While the app
// holds the host profile's socket, and after it disconnects, the person's own
// device asking for the room (the read and join both use Snapshot) is told
// the host's powers and invite path. Frames on the app's connection stay
// stripped, also after another API server's runtime state is adopted. Catches
// the strip keyed by who holds the seat instead of who is asking.
func TestOtherAppMarkFollowsTheConnection(t *testing.T) {
	now := time.Now()
	repo := &stubRepo{room: baseRoom(now)}
	s := newServiceForTest(now, repo, nil, nil, nil)
	t.Cleanup(s.Close)
	ownDevice := func(when string) {
		t.Helper()
		got, err := s.Snapshot(t.Context(), repo.room.ID, 7, "host")
		if err != nil {
			t.Fatal(err)
		}
		if !got.SelfCanManageRoom || !got.SelfCanControlTransport || got.InvitePath == "" {
			t.Fatalf("own device %s: manage %v, control %v, invite %q", when, got.SelfCanManageRoom, got.SelfCanControlTransport, got.InvitePath)
		}
	}
	frame := func(when string, powers bool) {
		t.Helper()
		s.mu.Lock()
		live := s.rooms[repo.room.ID]
		got := personalizeSnapshot(s.buildSnapshotLocked(live, 0, ""), live.room, live.members[buildMemberKey(7, "host")])
		s.mu.Unlock()
		if got.SelfCanManageRoom != powers || got.SelfCanControlTransport != powers || (got.InvitePath != "") != powers {
			t.Fatalf("frame %s: manage %v, control %v, invite %q", when, got.SelfCanManageRoom, got.SelfCanControlTransport, got.InvitePath)
		}
	}
	appFrame := func(when string) { t.Helper(); frame(when, false) }

	// While the person's device holds the seat the app cannot take it
	// (TestOtherAppDoesNotTakeAConnectedSeat), and the device's frames keep
	// the host's powers.
	device, _, err := s.Connect(t.Context(), repo.room.ID, 7, "host", new(replacementConn))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ConnectOtherApp(t.Context(), repo.room.ID, 7, "host", new(replacementConn)); !errors.Is(err, ErrMemberAlreadyConnected) {
		t.Fatalf("app took the device's seat: %v", err)
	}
	frame("on the device's connection", true)
	ownDevice("with its socket connected")
	s.Disconnect(device, false)

	reg, _, err := s.ConnectOtherApp(t.Context(), repo.room.ID, 7, "host", new(replacementConn))
	if err != nil {
		t.Fatal(err)
	}
	ownDevice("while the app holds the seat")
	appFrame("after connecting")

	s.mu.Lock()
	live := s.rooms[repo.room.ID]
	s.adoptRuntimeLocked(t.Context(), live, live.room, s.runtimeLocked(live))
	s.mu.Unlock()
	appFrame("after a runtime adoption")

	s.Disconnect(reg, false)
	ownDevice("after the app disconnected, without reconnecting")

	// The device's socket coming back is told the powers in the snapshot
	// connect returns and in its frames.
	_, back, err := s.Connect(t.Context(), repo.room.ID, 7, "host", new(replacementConn))
	if err != nil {
		t.Fatal(err)
	}
	if !back.SelfCanManageRoom || !back.SelfCanControlTransport || back.InvitePath == "" {
		t.Fatalf("device reconnect snapshot: %+v", back)
	}
	frame("on the device's returning connection", true)
}

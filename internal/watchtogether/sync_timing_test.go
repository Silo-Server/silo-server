package watchtogether

import (
	"testing"
	"time"
)

// A member that runs every command at its execute_at, projecting from it at
// 1x, is exactly where the room says it is. A slow member raises the lead for
// everyone; that must not show up as drift on the members who are in sync.
func TestMembersRunningCommandsOnTimeStayInSyncWithASlowMember(t *testing.T) {
	f := newBufferingRoom(t, "ann", "bob", "far")
	f.member("far").lastPingMS = 1800

	position := f.expected()
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPause, PositionSeconds: &position}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(5 * time.Second)
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPlay}); err != nil {
		t.Fatal(err)
	}
	play := lastTransport(t, f.conns["ann"])
	if play.Action != TransportActionPlay {
		t.Fatalf("room command = %+v, want play", play)
	}
	executeAt, err := time.Parse(time.RFC3339Nano, play.ExecuteAt)
	if err != nil {
		t.Fatal(err)
	}
	if lead := executeAt.Sub(f.now); lead != 1800*time.Millisecond {
		t.Fatalf("lead = %v, want the slow member's round trip", lead)
	}

	anchor, generation := f.repo.room.AnchorPositionSeconds, f.repo.room.Generation
	counts := map[string]int{}
	for profile, conn := range f.conns {
		counts[profile] = transportCount(conn)
	}
	for range 40 {
		f.now = f.now.Add(1500 * time.Millisecond)
		local := play.PositionSeconds + max(0, f.now.Sub(executeAt).Seconds())
		for profile := range f.conns {
			f.report(profile, local)
		}
	}
	for profile, conn := range f.conns {
		if got := transportCount(conn); got != counts[profile] {
			t.Fatalf("%s received %d corrections while in sync", profile, got-counts[profile])
		}
	}
	if f.repo.room.AnchorPositionSeconds != anchor || f.repo.room.Generation != generation {
		t.Fatalf("in-sync reports moved the room: anchor %v -> %v", anchor, f.repo.room.AnchorPositionSeconds)
	}
}

// A guest's stall may pause the room, but the room waits where it is: the
// other viewers stay put and only the stalled guest is moved, forward to the
// pause point, which the barrier holds it to.
func TestBufferingPauseMovesOnlyTheStalledMember(t *testing.T) {
	f := newBufferingRoom(t, "ann", "bob")
	f.now = f.now.Add(30 * time.Second)
	expected := f.expected()

	snapshot, err := f.s.HandleBufferingForConnection(t.Context(), f.reg("ann"), f.userID("ann"), "ann",
		StateReport{SessionID: "ann-session", PositionSeconds: expected - 3})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("stall did not pause the room: %s", snapshot.PlaybackState)
	}
	anchor := f.repo.room.AnchorPositionSeconds
	if anchor < expected {
		t.Fatalf("stall rewound the room from %v to %v", expected, anchor)
	}
	for _, profile := range []string{"host", "bob"} {
		command := lastTransport(t, f.conns[profile])
		if command.Action != TransportActionPause || command.PositionSeconds != anchor {
			t.Fatalf("%s command = %+v, want pause at %v", profile, command, anchor)
		}
	}
	seek := lastTransport(t, f.conns["ann"])
	if seek.Action != TransportActionSeek || seek.PlaybackState != RoomPlaybackStateWaiting || seek.PositionSeconds != anchor {
		t.Fatalf("stalled member command = %+v, want a waiting seek to %v", seek, anchor)
	}

	f.ready("host", anchor)
	f.ready("bob", anchor)
	if f.ready("ann", expected-3).PlaybackState != RoomPlaybackStateWaiting {
		t.Fatal("the stalled member's old position satisfied the barrier")
	}
	// A rebuilt stream may land a few seconds short; a ready naming the seek
	// proves it ran, and the member nudges the rest once the room plays.
	landed, err := f.s.HandleReadyForConnection(t.Context(), f.reg("ann"), f.userID("ann"), "ann",
		StateReport{SessionID: "ann-session", CommandID: seek.CommandID, PositionSeconds: anchor - 3})
	if err != nil {
		t.Fatal(err)
	}
	if landed.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatal("the room did not resume once the stalled member ran its seek")
	}

	// A reconnect during a later wait gets that wait's command, not this seek.
	f.member("ann").waitingCommand = &seek
	f.s.rooms[f.repo.room.ID].command = &TransportCommand{CommandID: "later", SelectionRevision: seek.SelectionRevision,
		Action: TransportActionPause, IssuedAt: f.now.Add(time.Minute).Format(time.RFC3339Nano)}
	if got := f.s.waitingCommandForLocked(f.s.rooms[f.repo.room.ID], "ann-session"); got.CommandID != "later" {
		t.Fatalf("waiting command = %+v, want the current wait's", got)
	}
}

// A report sent before a command ran describes the state the command
// replaces. It is not corrected, and once the command has settled a pause
// mismatch is corrected at once.
func TestReportsWhileACommandSettlesAreNotCorrected(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	position := f.expected()
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPause, PositionSeconds: &position}); err != nil {
		t.Fatal(err)
	}
	pause := lastTransport(t, f.conns["guest"])
	still := transportCount(f.conns["guest"])

	f.now = f.now.Add(200 * time.Millisecond)
	if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("guest"), f.userID("guest"), "guest",
		StateReport{SessionID: "guest-session", PositionSeconds: position + 0.2, IsPaused: false}); err != nil {
		t.Fatal(err)
	}
	if got := transportCount(f.conns["guest"]); got != still {
		t.Fatalf("a report racing the pause was corrected %d times", got-still)
	}

	f.now = f.now.Add(defaultTransportLead + commandSettleWindow)
	if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("guest"), f.userID("guest"), "guest",
		StateReport{SessionID: "guest-session", PositionSeconds: pause.PositionSeconds + 2, IsPaused: false}); err != nil {
		t.Fatal(err)
	}
	if command := lastTransport(t, f.conns["guest"]); command.Action != TransportActionPause || command.CommandID == pause.CommandID {
		t.Fatalf("a settled pause mismatch was not corrected: %+v", command)
	}
}

// One slow round trip does not move every command's lead, and no member can
// push the lead past maxTransportLead.
func TestMemberLatencyIsSmoothedAndBounded(t *testing.T) {
	f := newBufferingRoom(t)
	ping := func(ms int64) {
		t.Helper()
		if err := f.s.HandlePingForConnection(t.Context(), f.reg("host"), 7, "host", ms); err != nil {
			t.Fatal(err)
		}
	}
	ping(100)
	ping(900)
	if got := f.member("host").lastPingMS; got != 300 {
		t.Fatalf("smoothed ping = %d, want 300", got)
	}
	// Queueing only adds delay, so a faster round trip is taken at once.
	ping(80)
	if got := f.member("host").lastPingMS; got != 80 {
		t.Fatalf("ping after a faster sample = %d, want 80", got)
	}
	for range 40 {
		ping(60_000)
	}
	if got := f.s.highestPingLocked(f.s.rooms[f.repo.room.ID]); got > maxTransportLead || got < maxTransportLead-10*time.Millisecond {
		t.Fatalf("lead = %v, want at most and near the %v bound", got, maxTransportLead)
	}
}

// The room starts advancing when members run a play, so the anchor is taken at
// the command's execute_at and every command carries that position.
func TestPlayAndResumeAnchorTheRoomAtExecution(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").lastPingMS = 1200
	position := f.expected()
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPause, PositionSeconds: &position}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(5 * time.Second)
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPlay}); err != nil {
		t.Fatal(err)
	}
	assertAnchoredAtExecution := func(what string) {
		t.Helper()
		command := lastTransport(t, f.conns["guest"])
		executeAt, err := time.Parse(time.RFC3339Nano, command.ExecuteAt)
		if err != nil {
			t.Fatal(err)
		}
		if !f.repo.room.AnchorUpdatedAt.Equal(executeAt) {
			t.Fatalf("%s anchored at %v, want its execute_at %v", what, f.repo.room.AnchorUpdatedAt, executeAt)
		}
		for _, later := range []time.Duration{0, 3 * time.Second} {
			want := command.PositionSeconds + later.Seconds()
			if got := expectedPosition(f.repo.room, executeAt.Add(later)); got != want {
				t.Fatalf("%s: room at execute_at+%v = %v, want %v", what, later, got, want)
			}
		}
	}
	assertAnchoredAtExecution("play")

	// A seek's barrier resumes the same way.
	f.now = f.now.Add(10 * time.Second)
	target := 500.0
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionSeek, PositionSeconds: &target}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Second)
	f.resumeAll()
	assertAnchoredAtExecution("resume")
}

// A socket renewal during a stall wait resends the stalled member its own
// seek, so it cannot ready up where it stalled.
func TestReconcilerResendsTheStalledMembersSeek(t *testing.T) {
	f := newBufferingRoom(t, "ann", "bob")
	f.now = f.now.Add(30 * time.Second)
	if _, err := f.s.HandleBufferingForConnection(t.Context(), f.reg("ann"), f.userID("ann"), "ann",
		StateReport{SessionID: "ann-session", PositionSeconds: f.expected() - 3}); err != nil {
		t.Fatal(err)
	}
	seek := lastTransport(t, f.conns["ann"])
	f.member("ann").lastCommandID = "" // a renewed socket has delivered nothing yet
	if err := f.s.reconcileRoom(t.Context(), f.repo.room.ID); err != nil {
		t.Fatal(err)
	}
	if resent := lastTransport(t, f.conns["ann"]); resent.Action != TransportActionSeek || resent.CommandID != seek.CommandID {
		t.Fatalf("reconciler resent %+v, want the stall seek %s", resent, seek.CommandID)
	}
	if got := f.member("ann").waitingCommand; got == nil || got.CommandID != seek.CommandID {
		t.Fatalf("waiting command = %+v, want the stall seek", got)
	}
}

// A stalled host is followed: the room waits at the host's position, because
// the host's ready would re-anchor the room wherever its stream lands anyway.
func TestStalledHostPausesTheRoomAtItsPosition(t *testing.T) {
	f := newBufferingRoom(t, "ann")
	f.now = f.now.Add(30 * time.Second)
	stalledAt := f.expected() - 3
	if _, err := f.s.HandleBufferingForConnection(t.Context(), f.reg("host"), 7, "host",
		StateReport{SessionID: "host-session", PositionSeconds: stalledAt}); err != nil {
		t.Fatal(err)
	}
	if f.repo.room.PlaybackState != RoomPlaybackStateWaiting || f.repo.room.AnchorPositionSeconds != stalledAt {
		t.Fatalf("room %s at %v, want waiting at the host's %v", f.repo.room.PlaybackState, f.repo.room.AnchorPositionSeconds, stalledAt)
	}
	if command := lastTransport(t, f.conns["host"]); command.Action != TransportActionPause {
		t.Fatalf("host command = %+v, want the room pause", command)
	}
}

// A report naming the command its client last applied describes the state
// after that command, so a host's system pause is taken at once.
func TestReportNamingTheLatestCommandEndsTheSettleWindow(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPlay}); err != nil {
		t.Fatal(err)
	}
	play := lastTransport(t, f.conns["host"])
	f.now = f.now.Add(time.Second) // inside execute_at + commandSettleWindow
	if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("host"), 7, "host",
		StateReport{SessionID: "host-session", CommandID: play.CommandID, PositionSeconds: f.expected(), IsPaused: true}); err != nil {
		t.Fatal(err)
	}
	if f.repo.room.PlaybackState != RoomPlaybackStatePaused {
		t.Fatalf("host's pause after applying the play was held: room %s", f.repo.room.PlaybackState)
	}
}

// Drift measured before a correction says nothing about the state after it:
// a correction needs two fresh out-of-band reports, not one.
func TestCorrectionStartsTheStrikesOver(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	f.report("guest", f.expected()-3)
	f.report("guest", f.expected()-3)
	first := transportCount(f.conns["guest"])
	if first == 0 {
		t.Fatal("sustained drift was not corrected")
	}
	f.now = f.now.Add(guestCorrectionRetryInterval + time.Second)
	f.report("guest", f.expected()-2.5)
	if got := transportCount(f.conns["guest"]); got != first {
		t.Fatalf("one report after a correction was corrected again (%d corrections)", got)
	}
	f.report("guest", f.expected()-2.5)
	if got := transportCount(f.conns["guest"]); got != first+1 {
		t.Fatalf("two reports after a correction dispatched %d corrections, want 1", got-first)
	}
}

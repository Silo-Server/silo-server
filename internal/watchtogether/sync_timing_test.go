package watchtogether

import (
	"math"
	"testing"
	"time"
)

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

	// A stall seek left over from an earlier wait earns no extra tolerance.
	stale := seek
	live0 := f.s.rooms[f.repo.room.ID]
	savedRoom, savedCommand := live0.room, live0.command
	live0.room.PlaybackState = RoomPlaybackStateWaiting
	live0.command = &TransportCommand{CommandID: "next-wait", SelectionRevision: seek.SelectionRevision,
		Action: TransportActionPause, IssuedAt: f.now.Add(time.Minute).Format(time.RFC3339Nano)}
	if isStallSeekLocked(live0, &stale) {
		t.Fatal("a stall seek from an earlier wait counted as this wait's")
	}
	live0.room, live0.command = savedRoom, savedCommand

	// Once the room resumes, its play is the member's command, even when the
	// clock has not moved since the wait began.
	live := f.s.rooms[f.repo.room.ID]
	f.member("ann").waitingCommand = &seek
	if got := f.s.waitingCommandForLocked(live, "ann-session"); got.CommandID != live.command.CommandID || got.Action != TransportActionPlay {
		t.Fatalf("after the resume, waiting command = %+v, want the room's play", got)
	}

	// A reconnect during a later wait gets that wait's command, not this seek.
	live.room.PlaybackState = RoomPlaybackStateWaiting
	live.command = &TransportCommand{CommandID: "later", SelectionRevision: seek.SelectionRevision,
		Action: TransportActionPause, IssuedAt: f.now.Add(time.Minute).Format(time.RFC3339Nano)}
	if got := f.s.waitingCommandForLocked(live, "ann-session"); got.CommandID != "later" {
		t.Fatalf("waiting command = %+v, want the current wait's", got)
	}
}

// The room starts advancing when members run a play, so the anchor is taken at
// the command's execute_at and every command carries that position.
func TestPlayAndResumeAnchorTheRoomAtExecution(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").lastPingMS = 1200
	position := f.expected()
	f.hostRequest(TransportRequest{Action: TransportActionPause, PositionSeconds: &position})
	f.now = f.now.Add(5 * time.Second)
	f.hostRequest(TransportRequest{Action: TransportActionPlay})
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
	f.hostRequest(TransportRequest{Action: TransportActionSeek, PositionSeconds: &target})
	f.now = f.now.Add(2 * time.Second)
	f.resumeAll()
	assertAnchoredAtExecution("resume")
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

// A command scheduled after the lead drops never runs before the room's own
// play, which would start its member ahead of everyone.
func TestCommandsNeverRunBeforeTheRoomsOwnCommand(t *testing.T) {
	f := newBufferingRoom(t, "ann", "far")
	f.member("far").lastPingMS = 1800
	position := f.expected()
	f.hostRequest(TransportRequest{Action: TransportActionPause, PositionSeconds: &position})
	f.now = f.now.Add(5 * time.Second)
	f.hostRequest(TransportRequest{Action: TransportActionPlay})
	play := lastTransport(t, f.conns["ann"])
	if err := f.s.HandlePingForConnection(t.Context(), f.reg("far"), f.userID("far"), "far", 100); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(100 * time.Millisecond)
	f.s.mu.Lock()
	dispatches := f.s.syncMemberToRoomLocked(f.s.rooms[f.repo.room.ID], "ann-session")
	f.s.mu.Unlock()
	f.s.sendCommandDispatches(t.Context(), dispatches)
	sync := lastTransport(t, f.conns["ann"])
	if sync.CommandID == play.CommandID {
		t.Fatal("no sync was sent")
	}
	if sync.ExecuteAt < play.ExecuteAt || sync.PositionSeconds != play.PositionSeconds {
		t.Fatalf("sync %+v runs before the room's play %+v", sync, play)
	}
}

func (f *bufferingRoom) hostReport(commandID string, position float64, paused bool) {
	f.t.Helper()
	if _, err := f.s.HandleStateReportForConnection(f.t.Context(), f.reg("host"), 7, "host",
		StateReport{SessionID: "host-session", CommandID: commandID, PositionSeconds: position, IsPaused: paused}); err != nil {
		f.t.Fatal(err)
	}
}

// Snapshots show no member's commands, so a correction alone sends the room
// no snapshot on reconciliation.
func TestCorrectionsDoNotBroadcastSnapshots(t *testing.T) {
	f := newBufferingRoom(t, "ann", "bob")
	if err := f.s.reconcileRoom(t.Context(), f.repo.room.ID); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(10 * time.Second)
	snapshots := func() int {
		n := 0
		for _, p := range f.conns["bob"].payloads {
			if p[clusterMessageTypeKey] == snapshotMessageType {
				n++
			}
		}
		return n
	}
	for range guestCorrectionStrikes + 1 {
		f.report("ann", f.expected()-30)
	}
	if f.member("ann").correctionCommand == nil {
		t.Fatal("ann was not corrected")
	}
	before := snapshots()
	if before == 0 {
		t.Fatal("bob received no snapshots at all; the count is not measuring anything")
	}
	if err := f.s.reconcileRoom(t.Context(), f.repo.room.ID); err != nil {
		t.Fatal(err)
	}
	if got := snapshots(); got != before {
		t.Fatalf("a correction sent bob %d snapshots", got-before)
	}
}

// A press that undoes a play or pause still waiting for its execute_at comes
// from a member in the state before it: a pause canceling a pending play
// leaves the paused room where it was, and a play canceling a pending pause
// runs on to its own execute_at.
func TestRequestInsideTheLeadUsesTheStateBeforeThePendingCommand(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").lastPingMS = 2000
	request := func(action TransportAction, position float64) TransportCommand {
		t.Helper()
		f.hostRequest(TransportRequest{Action: action, PositionSeconds: &position, IsPaused: action == TransportActionPause})
		return lastTransport(t, f.conns["guest"])
	}
	leadAfter := func(command TransportCommand) float64 {
		t.Helper()
		executeAt, err := time.Parse(time.RFC3339Nano, command.ExecuteAt)
		if err != nil {
			t.Fatal(err)
		}
		return executeAt.Sub(f.now).Seconds()
	}

	request(TransportActionPause, 100)
	f.now = f.now.Add(5 * time.Second)
	request(TransportActionPlay, 100)
	f.now = f.now.Add(time.Second)
	if undo := request(TransportActionPause, 100); undo.PositionSeconds != 100 {
		t.Fatalf("pause canceling a pending play lands at %.2f, want the paused 100", undo.PositionSeconds)
	}

	f.now = f.now.Add(5 * time.Second)
	request(TransportActionPlay, 100)
	f.now = f.now.Add(5 * time.Second)
	position := f.expected()
	request(TransportActionPause, position)
	f.now = f.now.Add(time.Second)
	position++
	undo := request(TransportActionPlay, position)
	if want := position + leadAfter(undo); math.Abs(undo.PositionSeconds-want) > 0.01 {
		t.Fatalf("play canceling a pending pause lands at %.2f, want %.2f a lead after the still-playing requester", undo.PositionSeconds, want)
	}
}

func TestSyncingHostFarFromTheRoomDoesNotRewindIt(t *testing.T) {
	f, sync := newSyncingHost(t)
	f.now = f.now.Add(commandSettleWindow + time.Second)
	expected := f.expected()
	f.hostReport(sync.CommandID, expected-60, false)
	if got := f.expected(); math.Abs(got-expected) > 0.5 || !f.member("host").syncingToRoom {
		t.Fatalf("room moved from %.1f to %.1f (syncing %v)", expected, got, f.member("host").syncingToRoom)
	}
}

func newSyncingHost(t *testing.T) (*bufferingRoom, TransportCommand) {
	t.Helper()
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	f.s.mu.Lock()
	dispatches := f.s.syncMemberToRoomLocked(f.s.rooms[f.repo.room.ID], "host-session")
	f.member("host").syncingToRoom = true
	f.s.mu.Unlock()
	f.s.sendCommandDispatches(t.Context(), dispatches)
	return f, lastTransport(t, f.conns["host"])
}

func TestReportNamingItsCommandWaitsForAnUndeliveredRoomCommand(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	f.hostRequest(TransportRequest{Action: TransportActionPlay})
	play := lastTransport(t, f.conns["host"])
	f.now = f.now.Add(10 * time.Second)
	position := f.expected()
	f.hostRequest(TransportRequest{Action: TransportActionPause, PositionSeconds: &position})
	// As if the pause came from another server and was not yet forwarded here.
	host := f.member("host")
	host.lastCommandID, host.lastSentCommandID, host.lastSentPaused = play.CommandID, play.CommandID, false
	host.settleUntil = time.Time{}
	f.now = f.now.Add(500 * time.Millisecond)
	f.stateReport("host", StateReport{SessionID: "host-session", CommandID: play.CommandID, PositionSeconds: position + 0.5})
	if f.repo.room.PlaybackState != RoomPlaybackStatePaused {
		t.Fatalf("a report from before an undelivered pause resumed the room: %s", f.repo.room.PlaybackState)
	}
}

func TestReportNamingAnOlderCommandPredatesTheNewest(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	f.hostRequest(TransportRequest{Action: TransportActionPlay})
	older := lastTransport(t, f.conns["host"])
	f.now = f.now.Add(10 * time.Second)
	position := f.expected()
	f.hostRequest(TransportRequest{Action: TransportActionPause, PositionSeconds: &position})
	// Past the pause's settle window, a slow host's report still names the play.
	f.now = f.now.Add(4 * time.Second)
	f.stateReport("host", StateReport{SessionID: "host-session", CommandID: older.CommandID, PositionSeconds: position + 4})
	if f.repo.room.PlaybackState != RoomPlaybackStatePaused {
		t.Fatalf("a report from before the pause resumed the room: %s", f.repo.room.PlaybackState)
	}
}

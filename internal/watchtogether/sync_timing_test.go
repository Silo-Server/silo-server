package watchtogether

import (
	"math"
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

// A report naming the command its client last applied, with the play state
// that command set, describes the state after it and is taken at once. One
// that contradicts the command inside its settle window can trail the player
// by a sample, so it waits for the window to end.
func TestReportNamingTheLatestCommandEndsTheSettleWindow(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPlay}); err != nil {
		t.Fatal(err)
	}
	play := lastTransport(t, f.conns["guest"])
	hostReport := func(paused bool) {
		t.Helper()
		if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("host"), 7, "host",
			StateReport{SessionID: "host-session", CommandID: play.CommandID, PositionSeconds: f.expected(), IsPaused: paused}); err != nil {
			t.Fatal(err)
		}
	}
	guestReport := func() {
		t.Helper()
		if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("guest"), f.userID("guest"), "guest",
			StateReport{SessionID: "guest-session", CommandID: play.CommandID, PositionSeconds: f.expected() - 3}); err != nil {
			t.Fatal(err)
		}
	}

	// Matching the play, the guest's drift counts at once.
	f.now = f.now.Add(time.Second)
	before := transportCount(f.conns["guest"])
	for range guestCorrectionStrikes + 1 {
		guestReport()
	}
	if transportCount(f.conns["guest"]) == before {
		t.Fatal("drift reported after the guest ran the play was held by the settle window")
	}

	// A paused report naming the play, inside its window, is not taken.
	hostReport(true)
	if f.repo.room.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("a report contradicting the play inside its window moved the room to %s", f.repo.room.PlaybackState)
	}
	// After the window the same report is the host's decision.
	f.now = f.now.Add(commandSettleWindow + time.Second)
	hostReport(true)
	if f.repo.room.PlaybackState != RoomPlaybackStatePaused {
		t.Fatalf("the host's pause after the window was not taken: room %s", f.repo.room.PlaybackState)
	}
}

// A member that is not closing its drift is corrected; drift measured before a
// correction says nothing about the state after it, so the count starts over.
func TestCorrectionStartsTheStrikesOver(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	stuck := func() {
		t.Helper()
		for range guestCorrectionStrikes + 1 {
			f.report("guest", f.expected()-3)
		}
	}
	stuck()
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
	f.report("guest", f.expected()-2.5)
	if got := transportCount(f.conns["guest"]); got != first+1 {
		t.Fatalf("sustained drift after a correction dispatched %d corrections, want 1", got-first)
	}
}

// A member closing its drift by rate is left to it; correcting it again would
// only restart its convergence. One that stops closing it is corrected.
func TestConvergingMemberIsNotCorrectedAgain(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	drift := 3.0
	for range 20 { // about 30 s at 4 percent
		f.now = f.now.Add(1500 * time.Millisecond)
		f.report("guest", f.expected()-drift)
		drift -= 0.06
	}
	if got := transportCount(f.conns["guest"]); got != 0 {
		t.Fatalf("a converging member was corrected %d times", got)
	}
	for range 3 {
		f.now = f.now.Add(1500 * time.Millisecond)
		f.report("guest", f.expected()-drift)
	}
	if got := transportCount(f.conns["guest"]); got != 1 {
		t.Fatalf("a member that stopped converging got %d corrections, want 1", got)
	}
}

// A command scheduled after the lead drops never runs before the room's own
// play, which would start its member ahead of everyone.
func TestCommandsNeverRunBeforeTheRoomsOwnCommand(t *testing.T) {
	f := newBufferingRoom(t, "ann", "far")
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

// A report naming an older command than the member was last sent predates the
// newest one, however slow the member is, until namedCommandWait.
func TestReportNamingAnOlderCommandPredatesTheNewest(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPlay}); err != nil {
		t.Fatal(err)
	}
	older := lastTransport(t, f.conns["host"])
	f.now = f.now.Add(10 * time.Second)
	position := f.expected()
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPause, PositionSeconds: &position}); err != nil {
		t.Fatal(err)
	}
	// Past the pause's settle window, a slow host's report still names the play.
	f.now = f.now.Add(4 * time.Second)
	if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("host"), 7, "host",
		StateReport{SessionID: "host-session", CommandID: older.CommandID, PositionSeconds: position + 4}); err != nil {
		t.Fatal(err)
	}
	if f.repo.room.PlaybackState != RoomPlaybackStatePaused {
		t.Fatalf("a report from before the pause resumed the room: %s", f.repo.room.PlaybackState)
	}
}

// A report naming the member's last command settles early only if the room's
// latest command reached the member too; on another API server it may not have.
func TestReportNamingItsCommandWaitsForAnUndeliveredRoomCommand(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPlay}); err != nil {
		t.Fatal(err)
	}
	play := lastTransport(t, f.conns["host"])
	f.now = f.now.Add(10 * time.Second)
	position := f.expected()
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPause, PositionSeconds: &position}); err != nil {
		t.Fatal(err)
	}
	// As if the pause came from another server and was not yet forwarded here.
	host := f.member("host")
	host.lastCommandID, host.lastSentCommandID, host.lastSentPaused = play.CommandID, play.CommandID, false
	host.settleUntil = time.Time{}
	f.now = f.now.Add(500 * time.Millisecond)
	if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("host"), 7, "host",
		StateReport{SessionID: "host-session", CommandID: play.CommandID, PositionSeconds: position + 0.5}); err != nil {
		t.Fatal(err)
	}
	if f.repo.room.PlaybackState != RoomPlaybackStatePaused {
		t.Fatalf("a report from before an undelivered pause resumed the room: %s", f.repo.room.PlaybackState)
	}
}

// A host still closing a gap by rate after a sync has run it: a pause from its
// system controls is the room's again once the sync has settled.
func TestSyncingHostThatRanItsSyncCanPause(t *testing.T) {
	f, sync := newSyncingHost(t)
	f.now = f.now.Add(commandSettleWindow + time.Second)
	f.hostReport(sync.CommandID, f.expected(), false)
	if f.member("host").syncingToRoom {
		t.Fatal("a host playing its sync at the room's position is still syncing")
	}
	f.now = f.now.Add(1500 * time.Millisecond)
	f.hostReport(sync.CommandID, f.expected(), true)
	if f.repo.room.PlaybackState != RoomPlaybackStatePaused {
		t.Fatalf("the synced host's pause was not taken: room %s", f.repo.room.PlaybackState)
	}
}

// A host whose play was refused (a browser blocking autoplay after a reload)
// names its sync while paused. That is not a pause the room should follow.
func TestSyncingHostWhosePlayWasRefusedDoesNotPauseTheRoom(t *testing.T) {
	f, sync := newSyncingHost(t)
	before := transportCount(f.conns["host"])
	f.now = f.now.Add(commandSettleWindow + time.Second)
	f.hostReport(sync.CommandID, f.expected()-3, true)
	if f.repo.room.PlaybackState != RoomPlaybackStatePlaying || !f.member("host").syncingToRoom {
		t.Fatalf("a host that never started its sync paused the room: room %s, syncing %v",
			f.repo.room.PlaybackState, f.member("host").syncingToRoom)
	}
	if transportCount(f.conns["host"]) == before {
		t.Fatal("the paused host was not corrected back to the room")
	}
}

// A host whose sync correction never loaded names the sync from its old
// stream. Far from the room, that is not a decision to rewind everyone to.
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

func (f *bufferingRoom) hostReport(commandID string, position float64, paused bool) {
	f.t.Helper()
	if _, err := f.s.HandleStateReportForConnection(f.t.Context(), f.reg("host"), 7, "host",
		StateReport{SessionID: "host-session", CommandID: commandID, PositionSeconds: position, IsPaused: paused}); err != nil {
		f.t.Fatal(err)
	}
}

// A second play sent inside the lead by a member still paused where the
// first one was sent starts the room from the same point, not a lead later.
func TestRepeatedPlayInsideTheLeadKeepsThePausedPosition(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").lastPingMS = 2000
	position := 100.0
	request := func(action TransportAction) TransportCommand {
		t.Helper()
		// is_paused as web and Android send it: the state the request asks for.
		if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
			TransportRequest{Action: action, PositionSeconds: &position, IsPaused: action == TransportActionPause}); err != nil {
			t.Fatal(err)
		}
		return lastTransport(t, f.conns["guest"])
	}
	request(TransportActionPause)
	f.now = f.now.Add(5 * time.Second)
	first := request(TransportActionPlay)
	f.now = f.now.Add(100 * time.Millisecond)
	second := request(TransportActionPlay)
	if first.PositionSeconds != 100 || second.PositionSeconds != 100 {
		t.Fatalf("plays carry %.2f and %.2f, want both at the paused 100", first.PositionSeconds, second.PositionSeconds)
	}
}

// A member of a paused room cannot close drift by rate, so its first report
// out of sync is a strike, not progress.
func TestPausedRoomMemberIsCorrectedAfterTwoReports(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	position := 100.0
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPause, PositionSeconds: &position}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(10 * time.Second)
	before := transportCount(f.conns["guest"])
	for range guestCorrectionStrikes {
		f.now = f.now.Add(1500 * time.Millisecond)
		if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("guest"), f.userID("guest"), "guest",
			StateReport{SessionID: "guest-session", PositionSeconds: 103, IsPaused: true}); err != nil {
			t.Fatal(err)
		}
	}
	if got := transportCount(f.conns["guest"]) - before; got != 1 {
		t.Fatalf("a paused member 3 s off got %d corrections after %d reports, want 1", got, guestCorrectionStrikes)
	}
}

// The /api/v1 room socket keeps its host band: its clients may report a
// host's scrub instead of requesting it.
func TestLegacySocketHostKeepsTheNarrowBand(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		f := newBufferingRoom(t, "guest")
		f.now = f.now.Add(10 * time.Second)
		var conn RoomConnection = f.conns["host"]
		if legacy {
			conn = legacyRoomConn{f.conns["host"]}
		}
		f.member("host").connection = conn
		expected := f.expected()
		if _, err := f.s.HandleStateReportForConnection(t.Context(), registrationFor(f.repo.room.ID, 7, "host", conn), 7, "host",
			StateReport{SessionID: "host-session", PositionSeconds: expected + 4}); err != nil {
			t.Fatal(err)
		}
		moved := math.Abs(f.expected()-expected) > 2
		if moved != legacy {
			t.Fatalf("legacy socket %v: a 4 s host scrub moved the room = %v", legacy, moved)
		}
	}
}

type legacyRoomConn struct{ *recordingConn }

func (legacyRoomConn) LegacyRoomProtocol() bool { return true }

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

// A burst of relative skips from a client whose position has not caught up
// with the pending seek repeats that seek's target. The repeat is the same
// seek, so the barrier does not restart, and a seek during the barrier keeps
// whether the room resumes playing.
func TestRepeatedSeekDuringABarrierIsTheSameSeek(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	seek := func(target float64, paused bool) {
		t.Helper()
		if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
			TransportRequest{Action: TransportActionSeek, PositionSeconds: &target, IsPaused: paused}); err != nil {
			t.Fatal(err)
		}
	}
	seek(140, false)
	first := lastTransport(t, f.conns["guest"])
	generation := f.repo.room.Generation
	f.ready("guest", 140)

	// The host is paused by the barrier now; its repeat carries that.
	f.now = f.now.Add(300 * time.Millisecond)
	seek(140.3, true)
	if got := lastTransport(t, f.conns["guest"]); got.CommandID != first.CommandID || f.repo.room.Generation != generation {
		t.Fatalf("a repeat of the pending seek restarted the barrier: %+v", got)
	}
	if !f.member("guest").isReady {
		t.Fatal("a repeat of the pending seek reset readiness")
	}

	// A real further skip restarts it, and the room still resumes playing.
	seek(170, true)
	second := lastTransport(t, f.conns["guest"])
	if second.CommandID == first.CommandID || second.PositionSeconds != 170 {
		t.Fatalf("a further skip was dropped: %+v", second)
	}
	f.resumeAll()
	if f.repo.room.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("seeks during the barrier left the room %s, want playing", f.repo.room.PlaybackState)
	}
}

// A paused member that cannot apply a correction in place (it stays where the
// correction found it) is not corrected again and again.
func TestPausedMemberThatCannotMoveIsNotCorrectedAgain(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	position := f.expected()
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionPause, PositionSeconds: &position}); err != nil {
		t.Fatal(err)
	}
	pause := lastTransport(t, f.conns["guest"])
	report := func(at float64) {
		t.Helper()
		if _, err := f.s.HandleStateReportForConnection(t.Context(), f.reg("guest"), f.userID("guest"), "guest",
			StateReport{SessionID: "guest-session", CommandID: pause.CommandID, PositionSeconds: at, IsPaused: true}); err != nil {
			t.Fatal(err)
		}
	}
	f.now = f.now.Add(commandSettleWindow + time.Second)
	stuck := pause.PositionSeconds - 1.6
	before := transportCount(f.conns["guest"])
	for range guestCorrectionStrikes + 1 {
		report(stuck)
	}
	corrections := transportCount(f.conns["guest"])
	if corrections != before+1 {
		t.Fatalf("the first correction was not sent (%d)", corrections-before)
	}
	for range 4 { // about 46 s, inside stuckCorrectionRetry
		f.now = f.now.Add(guestCorrectionRetryInterval + commandSettleWindow)
		report(stuck)
		report(stuck)
	}
	if got := transportCount(f.conns["guest"]); got != corrections {
		t.Fatalf("a paused member that cannot move got %d more corrections", got-corrections)
	}
	// A failure to apply a correction can pass, so it is retried after a while.
	f.now = f.now.Add(stuckCorrectionRetry - 4*(guestCorrectionRetryInterval+commandSettleWindow))
	report(stuck)
	report(stuck)
	if got := transportCount(f.conns["guest"]); got != corrections+1 {
		t.Fatalf("a paused member stuck for %v got %d corrections, want 1 retry", stuckCorrectionRetry, got-corrections)
	}
	corrections++
	// Once it moves, it is corrected again if it is still off.
	f.now = f.now.Add(guestCorrectionRetryInterval)
	for range guestCorrectionStrikes + 1 {
		report(stuck - 2)
	}
	if got := transportCount(f.conns["guest"]); got != corrections+1 {
		t.Fatalf("a paused member that moved further off got %d corrections, want 1", got-corrections)
	}
}

// A host landing that moved the barrier's destination makes a seek back to the
// original target a real seek, not a repeat.
func TestSeekBackToTargetAfterAHostLandingIsNotARepeat(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	target := 1500.0
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionSeek, PositionSeconds: &target}); err != nil {
		t.Fatal(err)
	}
	seek := lastTransport(t, f.conns["host"])
	if _, err := f.s.HandleReadyForConnection(t.Context(), f.reg("host"), 7, "host",
		StateReport{SessionID: "host-session", CommandID: seek.CommandID, PositionSeconds: 1494, IsPaused: true}); err != nil {
		t.Fatal(err)
	}
	if f.repo.room.AnchorPositionSeconds != 1494 {
		t.Fatalf("host landing did not move the destination: %v", f.repo.room.AnchorPositionSeconds)
	}
	if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
		TransportRequest{Action: TransportActionSeek, PositionSeconds: &target}); err != nil {
		t.Fatal(err)
	}
	if again := lastTransport(t, f.conns["guest"]); again.CommandID == seek.CommandID || again.PositionSeconds != 1500 {
		t.Fatalf("the seek back to 1500 was dropped as a repeat: %+v", again)
	}
}

// A stalled host pauses the room where it is, within the bound measured from
// where the room is now; the scheduling lead does not narrow it.
func TestStalledHostWithinTheBoundPausesTheRoomAtItsPosition(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").lastPingMS = 2000
	f.now = f.now.Add(30 * time.Second)
	stalledAt := f.expected() - 4
	if _, err := f.s.HandleBufferingForConnection(t.Context(), f.reg("host"), 7, "host",
		StateReport{SessionID: "host-session", PositionSeconds: stalledAt}); err != nil {
		t.Fatal(err)
	}
	if anchor := f.repo.room.AnchorPositionSeconds; anchor != stalledAt {
		t.Fatalf("a host stalled 4 s behind paused the room at %.2f, want its position %.2f", anchor, stalledAt)
	}
}

// A pause comes from a playing member, which runs on until the command
// executes, so the pause lands a lead after the press, also when a second
// pause follows inside the lead.
func TestPauseFromAPlayingMemberRunsOnToExecuteAt(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").lastPingMS = 2000
	f.now = f.now.Add(30 * time.Second)
	pause := func() TransportCommand {
		t.Helper()
		position := f.expected()
		if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
			TransportRequest{Action: TransportActionPause, PositionSeconds: &position, IsPaused: true}); err != nil {
			t.Fatal(err)
		}
		command := lastTransport(t, f.conns["guest"])
		executeAt, err := time.Parse(time.RFC3339Nano, command.ExecuteAt)
		if err != nil {
			t.Fatal(err)
		}
		if want := position + executeAt.Sub(f.now).Seconds(); math.Abs(command.PositionSeconds-want) > 0.01 {
			t.Fatalf("pause from %.2f lands at %.2f, want %.2f a lead later", position, command.PositionSeconds, want)
		}
		return command
	}
	pause()
	f.now = f.now.Add(100 * time.Millisecond)
	pause()
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
		if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host",
			TransportRequest{Action: action, PositionSeconds: &position, IsPaused: action == TransportActionPause}); err != nil {
			t.Fatal(err)
		}
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

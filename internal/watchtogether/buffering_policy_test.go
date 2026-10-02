package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// bufferingRoom is a playing room with a host and guests, each attached to a
// playback session, driven by a clock the test advances.
type bufferingRoom struct {
	t     *testing.T
	s     *Service
	repo  *stubRepo
	now   time.Time
	conns map[string]*recordingConn
}

func newBufferingRoom(t *testing.T, guests ...string) *bufferingRoom {
	t.Helper()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	room := baseRoom(now)
	room.AnchorUpdatedAt = now
	room.AnchorPositionSeconds = 100
	f := &bufferingRoom{t: t, repo: &stubRepo{room: room}, now: now, conns: make(map[string]*recordingConn)}
	f.s = newServiceForTest(now, f.repo, &stubSessions{}, &stubFiles{}, nil)
	f.s.now = func() time.Time { return f.now }
	t.Cleanup(f.s.Close)
	live := f.s.rooms[room.ID]
	add := func(userID int, profileID string) {
		conn := new(recordingConn)
		f.conns[profileID] = conn
		live.members[buildMemberKey(userID, profileID)] = &memberState{
			userID: userID, profileID: profileID, sessionID: profileID + "-session", connection: conn, isReady: true,
		}
	}
	add(7, "host")
	for i, guest := range guests {
		add(8+i, guest)
	}
	return f
}

func (f *bufferingRoom) userID(profileID string) int {
	return f.member(profileID).userID
}

func (f *bufferingRoom) member(profileID string) *memberState {
	for _, member := range f.s.rooms[f.repo.room.ID].members {
		if member.profileID == profileID {
			return member
		}
	}
	f.t.Fatalf("no member %q", profileID)
	return nil
}

func (f *bufferingRoom) reg(profileID string) *Registration {
	return registrationFor(f.repo.room.ID, f.userID(profileID), profileID, f.conns[profileID])
}

func (f *bufferingRoom) expected() float64 {
	return expectedPosition(f.repo.room, f.now)
}

func (f *bufferingRoom) buffer(profileID string) Snapshot {
	f.t.Helper()
	snapshot, err := f.s.HandleBufferingForConnection(f.t.Context(), f.reg(profileID), f.userID(profileID), profileID, StateReport{
		SessionID: profileID + "-session", PositionSeconds: f.expected(),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return snapshot
}

func (f *bufferingRoom) ready(profileID string, position float64) Snapshot {
	f.t.Helper()
	snapshot, err := f.s.HandleReadyForConnection(f.t.Context(), f.reg(profileID), f.userID(profileID), profileID, StateReport{
		SessionID: profileID + "-session", PositionSeconds: position,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return snapshot
}

func (f *bufferingRoom) report(profileID string, position float64) Snapshot {
	f.t.Helper()
	snapshot, err := f.s.HandleStateReportForConnection(f.t.Context(), f.reg(profileID), f.userID(profileID), profileID, StateReport{
		SessionID: profileID + "-session", PositionSeconds: position,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return snapshot
}

// resumeAll acknowledges the current barrier for every member.
func (f *bufferingRoom) resumeAll() {
	f.t.Helper()
	position := f.repo.room.AnchorPositionSeconds
	for _, member := range f.s.rooms[f.repo.room.ID].members {
		f.ready(member.profileID, position)
	}
	if f.repo.room.PlaybackState != RoomPlaybackStatePlaying {
		f.t.Fatalf("room did not resume: %s", f.repo.room.PlaybackState)
	}
}

func (f *bufferingRoom) missDeadline() {
	f.t.Helper()
	f.now = f.now.Add(waitingResumeDeadline)
	f.s.handleWaitingDeadline(f.repo.room.ID, f.s.rooms[f.repo.room.ID].waitingEpoch)
	if f.repo.room.PlaybackState != RoomPlaybackStatePlaying {
		f.t.Fatalf("deadline did not resume the room: %s", f.repo.room.PlaybackState)
	}
}

func memberSummary(t *testing.T, snapshot Snapshot, profileID string) MemberSummary {
	t.Helper()
	for _, member := range snapshot.Members {
		if member.ProfileID == profileID {
			return member
		}
	}
	t.Fatalf("snapshot has no member %q", profileID)
	return MemberSummary{}
}

func TestRoomWaitsForAViewerOncePerStallCooldown(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	if snapshot := f.buffer("guest"); snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("first stall did not pause the room: %+v", snapshot)
	}
	f.resumeAll()

	f.now = f.now.Add(2 * time.Minute)
	generation := f.repo.room.Generation
	snapshot := f.buffer("guest")
	if snapshot.PlaybackState != RoomPlaybackStatePlaying || f.repo.room.Generation != generation {
		t.Fatalf("repeat stall paused the room again: %+v", snapshot)
	}
	if guest := memberSummary(t, snapshot, "guest"); !guest.IsBuffering || guest.IsSyncing {
		t.Fatalf("stalled guest status = %+v", guest)
	}
	if !f.member("guest").ignoreWait {
		t.Fatal("stalled guest is not catching up")
	}

	// Recovery keeps the cooldown: the next stall still does not pause.
	f.ready("guest", f.expected())
	f.now = f.now.Add(time.Minute)
	if snapshot := f.buffer("guest"); snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("stall after recovery paused the room: %+v", snapshot)
	}

	// Five stall-free minutes restore one wait.
	f.ready("guest", f.expected())
	f.now = f.now.Add(memberStallCooldown)
	if snapshot := f.buffer("guest"); snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("stall after the cooldown did not pause the room: %+v", snapshot)
	}
}

func TestBufferingPausesAreSpacedAcrossViewers(t *testing.T) {
	f := newBufferingRoom(t, "ann", "bob", "cat")
	f.buffer("ann")
	f.resumeAll()

	f.now = f.now.Add(bufferingWaitSpacing / 2)
	if snapshot := f.buffer("bob"); snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("second viewer paused the room within the spacing: %+v", snapshot)
	}
	if !f.member("bob").ignoreWait {
		t.Fatal("viewer skipped by the spacing is not catching up")
	}

	f.now = f.now.Add(bufferingWaitSpacing / 2)
	if snapshot := f.buffer("cat"); snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("stall after the spacing did not pause the room: %+v", snapshot)
	}
}

func TestViewerSkippedAtDeadlineCatchesUpWithoutPausingTheRoom(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.buffer("guest")
	f.ready("host", f.repo.room.AnchorPositionSeconds)
	f.missDeadline()
	guest := f.member("guest")
	if !guest.ignoreWait || !guest.isBuffering {
		t.Fatalf("skipped guest = %+v", guest)
	}

	f.now = f.now.Add(time.Minute)
	generation := f.repo.room.Generation
	if snapshot := f.buffer("guest"); snapshot.PlaybackState != RoomPlaybackStatePlaying || f.repo.room.Generation != generation {
		t.Fatalf("skipped guest paused the room again: %+v", snapshot)
	}
}

func TestRecoveryAfterDeadlineClearsBufferingAndTargetsTheRoom(t *testing.T) {
	for _, paused := range []bool{false, true} {
		name := "playing"
		if paused {
			name = "paused"
		}
		t.Run(name, func(t *testing.T) {
			f := newBufferingRoom(t, "guest")
			f.buffer("guest")
			f.ready("host", f.repo.room.AnchorPositionSeconds)
			f.missDeadline()
			if paused {
				position := f.expected()
				if _, err := f.s.HandleTransportRequestForConnection(t.Context(), f.reg("host"), 7, "host", TransportRequest{Action: TransportActionPause, PositionSeconds: &position}); err != nil {
					t.Fatal(err)
				}
			}
			f.now = f.now.Add(20 * time.Second)
			frames := len(f.conns["guest"].payloads)

			snapshot := f.ready("guest", f.repo.room.AnchorPositionSeconds)
			guest := memberSummary(t, snapshot, "guest")
			if guest.IsBuffering || !guest.IsReady || snapshot.SelfIgnoreWait {
				t.Fatalf("recovered guest = %+v ignore=%v", guest, snapshot.SelfIgnoreWait)
			}
			if f.member("guest").lastStallAt.IsZero() {
				t.Fatal("recovery forgot the stall cooldown")
			}
			if paused {
				if snapshot.PlaybackState != RoomPlaybackStatePaused {
					t.Fatalf("recovery changed the paused room: %+v", snapshot)
				}
				return
			}
			if len(f.conns["guest"].payloads) == frames {
				t.Fatal("recovered guest received no target")
			}
			command := lastTransport(t, f.conns["guest"])
			if command.Action != TransportActionPlay || command.SessionID != "guest-session" || command.PositionSeconds != f.expected() {
				t.Fatalf("recovery target = %+v, want play at %v", command, f.expected())
			}
		})
	}
}

func TestBufferingViewerIsNotCorrectedUntilTheHoldExpires(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").lastStallAt = f.now
	f.buffer("guest")
	frames := len(f.conns["guest"].payloads)

	f.now = f.now.Add(5 * time.Second)
	f.report("guest", 100)
	if got := len(f.conns["guest"].payloads); got != frames {
		t.Fatalf("stalled guest received %d corrections", got-frames)
	}

	f.now = f.now.Add(bufferingCorrectionHold)
	f.report("guest", 100)
	if command := lastTransport(t, f.conns["guest"]); command.PositionSeconds != f.expected() {
		t.Fatalf("correction after the hold = %+v", command)
	}
}

func TestStalledHostDoesNotRewindTheRoom(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("host").lastStallAt = f.now
	if snapshot := f.buffer("host"); snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("host within cooldown paused the room: %+v", snapshot)
	}
	f.now = f.now.Add(10 * time.Second)
	anchor := f.repo.room.AnchorPositionSeconds
	f.report("host", 100)
	if f.repo.room.AnchorPositionSeconds != anchor {
		t.Fatalf("stalled host moved the anchor to %v", f.repo.room.AnchorPositionSeconds)
	}

	// Recovery syncs the host to the room; until it arrives its reports are
	// corrected rather than followed.
	f.ready("host", 100)
	if !f.member("host").syncingToRoom {
		t.Fatal("recovered host is not syncing to the room")
	}
	f.now = f.now.Add(time.Second)
	f.report("host", 100.5)
	if f.repo.room.AnchorPositionSeconds != anchor {
		t.Fatalf("recovering host moved the anchor to %v", f.repo.room.AnchorPositionSeconds)
	}
	if command := lastTransport(t, f.conns["host"]); command.PositionSeconds != f.expected() {
		t.Fatalf("recovering host correction = %+v", command)
	}
}

func TestHostDriftInsideTheCatchUpBandIsCorrected(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.now = f.now.Add(10 * time.Second)
	anchor := f.repo.room.AnchorPositionSeconds
	f.report("host", f.expected()-1.8)
	if f.repo.room.AnchorPositionSeconds != anchor {
		t.Fatalf("host drift inside the band moved the anchor to %v", f.repo.room.AnchorPositionSeconds)
	}
	if command := lastTransport(t, f.conns["host"]); command.PositionSeconds != f.expected() {
		t.Fatalf("host drift correction = %+v", command)
	}

	f.report("host", f.expected()+30)
	if f.repo.room.AnchorPositionSeconds != f.expected() {
		t.Fatalf("host jump did not move the anchor: %v", f.repo.room.AnchorPositionSeconds)
	}
}

func TestGuestDepartureKeepsTheRoomPlaying(t *testing.T) {
	f := newBufferingRoom(t, "ann", "bob")
	f.s.Disconnect(f.reg("bob"), true)
	if f.repo.room.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("guest departure changed the room: %s", f.repo.room.PlaybackState)
	}
	if snapshot := f.buffer("ann"); snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("remaining guest's first stall did not pause the room: %+v", snapshot)
	}
}

func TestReconnectKeepsTheStallCooldown(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.s.sessions = &stubSessions{session: &playback.Session{UserID: 8, ProfileID: "guest", MediaFileID: 1}}
	f.s.files = &stubFiles{file: &models.MediaFile{ContentID: "movie-1"}}
	f.buffer("guest")
	f.resumeAll()

	f.s.Disconnect(f.reg("guest"), false)
	conn := new(recordingConn)
	reg, _, err := f.s.Connect(t.Context(), f.repo.room.ID, 8, "guest", conn)
	if err != nil {
		t.Fatal(err)
	}
	f.conns["guest"] = conn
	if _, err = f.s.AttachSessionForConnection(t.Context(), reg, 8, "guest", "guest-session"); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if snapshot := f.buffer("guest"); snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("reconnect reset the stall cooldown: %+v", snapshot)
	}
}

func TestNewSelectionClearsStallHistory(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.buffer("guest")
	f.s.selectionResolver = &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie-2"}}
	if _, err := f.s.SelectItem(t.Context(), f.repo.room.ID, 7, "host", SelectItemInput{ContentID: "movie-2"}); err != nil {
		t.Fatal(err)
	}
	live := f.s.rooms[f.repo.room.ID]
	if !live.bufferingWaitAt.IsZero() || !f.member("guest").lastStallAt.IsZero() {
		t.Fatalf("new selection kept stall history: room=%v guest=%v", live.bufferingWaitAt, f.member("guest").lastStallAt)
	}
}

// Stall cooldowns and buffering spacing live in the shared runtime, so a
// viewer cannot pause the room again by reaching another API server.
func TestStallHistoryIsSharedAcrossNodesPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	// Advance like a running cluster: each node's reconciler renews the leases
	// of the sockets it holds.
	advance := func(d time.Duration) {
		t.Helper()
		for step := 10 * time.Second; d > 0; d -= step {
			f.now = f.now.Add(min(step, d))
			for _, node := range []*Service{f.host, f.guest} {
				if err := node.reconcileRoom(t.Context(), f.roomID); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// The fixture's guest joined a playing room, which starts its cooldown.
	advance(memberStallCooldown)
	snapshot, err := f.guest.HandleBufferingForConnection(t.Context(), f.guestReg, 8, "guest", StateReport{SessionID: "guest-session", PositionSeconds: 20})
	if err != nil || snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("first stall did not pause the room: %+v %v", snapshot, err)
	}
	if _, err = f.host.HandleReadyForConnection(t.Context(), f.hostReg, 7, "host", StateReport{SessionID: "host-session", PositionSeconds: snapshot.AnchorPositionSeconds}); err != nil {
		t.Fatal(err)
	}
	if snapshot, err = f.guest.HandleReadyForConnection(t.Context(), f.guestReg, 8, "guest", StateReport{SessionID: "guest-session", PositionSeconds: snapshot.AnchorPositionSeconds}); err != nil || snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("room did not resume: %+v %v", snapshot, err)
	}

	advance(bufferingWaitSpacing / 2)
	snapshot, err = f.host.HandleBufferingForConnection(t.Context(), f.hostReg, 7, "host", StateReport{SessionID: "host-session", PositionSeconds: 30})
	if err != nil || snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("another node ignored the buffering spacing: %+v %v", snapshot, err)
	}

	advance(bufferingWaitSpacing)
	f.host.sessions = &stubSessions{session: &playback.Session{UserID: 8, ProfileID: "guest", MediaFileID: 1}}
	reg, _, err := f.host.Connect(t.Context(), f.roomID, 8, "guest", new(recordingConn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.AttachSessionForConnection(t.Context(), reg, 8, "guest", "guest-session"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = f.host.HandleBufferingForConnection(t.Context(), reg, 8, "guest", StateReport{SessionID: "guest-session", PositionSeconds: 90})
	if err != nil || snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("reconnecting through another node reset the stall cooldown: %+v %v", snapshot, err)
	}
}

func TestLoneViewerStallAlwaysPausesTheRoom(t *testing.T) {
	f := newBufferingRoom(t)
	f.member("host").lastStallAt = f.now
	f.member("host").ignoreWait = true
	snapshot := f.buffer("host")
	if snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("lone viewer's stall let the room run on: %+v", snapshot)
	}
	if f.member("host").ignoreWait || !f.s.rooms[f.repo.room.ID].bufferingWaitAt.IsZero() {
		t.Fatal("lone viewer's stall excluded them or spaced out other viewers")
	}
	if command := lastTransport(t, f.conns["host"]); command.Action != TransportActionPause {
		t.Fatalf("lone viewer was not paused: %+v", command)
	}
	if snapshot := f.ready("host", f.repo.room.AnchorPositionSeconds); snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("lone viewer's recovery did not resume the room: %+v", snapshot)
	}
}

// With nobody ready, running the room would only skip content. A lone viewer
// is waited for past the deadline, and reconnecting finds the room where they
// stalled.
func TestLoneViewerIsWaitedForPastTheDeadline(t *testing.T) {
	f := newBufferingRoom(t)
	f.s.sessions = &stubSessions{session: &playback.Session{UserID: 7, ProfileID: "host", MediaFileID: 1}}
	f.s.files = &stubFiles{file: &models.MediaFile{ContentID: "movie-1"}}
	f.buffer("host")
	f.now = f.now.Add(waitingResumeDeadline + time.Second)
	f.s.handleWaitingDeadline(f.repo.room.ID, f.s.rooms[f.repo.room.ID].waitingEpoch)
	if f.repo.room.PlaybackState != RoomPlaybackStateWaiting || f.member("host").ignoreWait {
		t.Fatalf("deadline ran the room without its only viewer: state=%s", f.repo.room.PlaybackState)
	}

	f.s.Disconnect(f.reg("host"), false)
	conn := new(recordingConn)
	reg, _, err := f.s.Connect(t.Context(), f.repo.room.ID, 7, "host", conn)
	if err != nil {
		t.Fatal(err)
	}
	f.conns["host"] = conn
	if _, err = f.s.AttachSessionForConnection(t.Context(), reg, 7, "host", "host-session"); err != nil {
		t.Fatal(err)
	}
	if command := lastTransport(t, conn); command.PlaybackState != RoomPlaybackStateWaiting || command.PositionSeconds != 100 {
		t.Fatalf("reconnected lone viewer target = %+v, want the waiting anchor 100", command)
	}
	f.now = f.now.Add(20 * time.Second)
	if snapshot := f.ready("host", 100); snapshot.PlaybackState != RoomPlaybackStatePlaying || f.repo.room.AnchorPositionSeconds != 100 {
		t.Fatalf("lone viewer's recovery moved the room: %+v", snapshot)
	}
}

// A viewer skipped while someone else watched can end up alone. Recovering
// then resumes the room from where they are instead of jumping them ahead.
func TestViewerLeftAloneRecoversFromTheirPosition(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.buffer("host")
	f.ready("guest", f.repo.room.AnchorPositionSeconds)
	f.missDeadline()
	if !f.member("host").ignoreWait {
		t.Fatal("deadline did not skip the stalled viewer")
	}
	f.s.Disconnect(f.reg("guest"), true)
	f.now = f.now.Add(20 * time.Second)
	f.ready("host", 100)
	if f.repo.room.AnchorPositionSeconds != 100 || f.repo.room.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("recovery jumped the room: anchor=%v state=%s", f.repo.room.AnchorPositionSeconds, f.repo.room.PlaybackState)
	}
	if command := lastTransport(t, f.conns["host"]); command.Action != TransportActionPlay || command.PositionSeconds != 100 {
		t.Fatalf("recovery target = %+v, want play at 100", command)
	}
}

// Past the deadline, the first viewer to become ready resumes the room.
func TestFirstReadyViewerResumesARoomPastItsDeadline(t *testing.T) {
	f := newBufferingRoom(t, "ann", "bob")
	f.buffer("ann")
	f.now = f.now.Add(waitingResumeDeadline + time.Second)
	f.s.handleWaitingDeadline(f.repo.room.ID, f.s.rooms[f.repo.room.ID].waitingEpoch)
	if f.repo.room.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatal("room resumed with nobody ready")
	}
	if snapshot := f.ready("bob", f.repo.room.AnchorPositionSeconds); snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("first ready viewer did not resume the room: %+v", snapshot)
	}
	if !f.member("ann").ignoreWait || !f.member("host").ignoreWait {
		t.Fatal("unready viewers were not skipped")
	}
}

// Clients that never acknowledge recovery still report positions; one that
// matches the room clears the member's buffering and catching-up flags.
func TestInSyncReportClearsAStaleBufferingStatus(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").lastStallAt = f.now
	f.buffer("guest")
	stallAt := f.member("guest").lastStallAt

	f.now = f.now.Add(5 * time.Second)
	f.report("guest", f.expected()-3)
	if guest := f.member("guest"); !guest.isBuffering || !guest.ignoreWait {
		t.Fatalf("out-of-sync report cleared the stall: %+v", guest)
	}

	hostFrames := len(f.conns["host"].payloads)
	snapshot := f.report("guest", f.expected()-0.5)
	guest := memberSummary(t, snapshot, "guest")
	if guest.IsBuffering || !guest.IsReady || snapshot.SelfIgnoreWait {
		t.Fatalf("in-sync report left the guest flagged: %+v ignore=%v", guest, snapshot.SelfIgnoreWait)
	}
	if len(f.conns["host"].payloads) == hostFrames {
		t.Fatal("other viewers did not receive the recovered status")
	}
	if !f.member("guest").lastStallAt.Equal(stallAt) {
		t.Fatal("recovery by report forgot the stall cooldown")
	}
}

// A late joiner is never asked to acknowledge anything while the room plays;
// reaching the room's position is what makes it ready.
func TestLateJoinerBecomesReadyOnceInSync(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("guest").isReady = false
	f.now = f.now.Add(3 * time.Second)
	f.report("guest", f.expected()-4)
	if f.member("guest").isReady {
		t.Fatal("out-of-sync joiner became ready")
	}
	hostFrames := len(f.conns["host"].payloads)
	snapshot := f.report("guest", f.expected()-0.2)
	if guest := memberSummary(t, snapshot, "guest"); !guest.IsReady || guest.IsBuffering {
		t.Fatalf("in-sync joiner status = %+v", guest)
	}
	if len(f.conns["host"].payloads) == hostFrames {
		t.Fatal("other viewers did not receive the joiner's ready status")
	}
}

func TestRolledBackBufferingPauseLeavesNoSpacingPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	failure := errors.New("abort room operation")
	_, err := withRoomOperation(t.Context(), f.host, f.roomID, func(ctx context.Context) (Snapshot, error) {
		snapshot, err := f.host.handleBufferingForConnection(ctx, f.hostReg, 7, "host", StateReport{SessionID: "host-session", PositionSeconds: 20})
		if err != nil {
			return Snapshot{}, err
		}
		if snapshot.PlaybackState != RoomPlaybackStateWaiting {
			t.Fatalf("stall did not pause the room: %+v", snapshot)
		}
		return Snapshot{}, failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if at := f.host.rooms[f.roomID].bufferingWaitAt; !at.IsZero() {
		t.Fatalf("rolled back pause left buffering spacing at %v", at)
	}
}

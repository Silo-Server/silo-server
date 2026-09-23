package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func lastTransport(t *testing.T, conn *recordingConn) TransportCommand {
	t.Helper()
	for i := len(conn.payloads) - 1; i >= 0; i-- {
		if cmd, ok := conn.payloads[i]["command"].(TransportCommand); ok {
			return cmd
		}
	}
	t.Fatal("no transport command received")
	return TransportCommand{}
}

type roomClusterFixture struct {
	repo                *Repository
	host, guest         *Service
	hostConn, guestConn *recordingConn
	hostReg, guestReg   *Registration
	now                 time.Time
	roomID              string
}

func newRoomClusterFixture(t *testing.T) *roomClusterFixture {
	t.Helper()
	// Postgres stores timestamptz at microsecond precision. Keep the service
	// clock aligned so persisting an anchor does not introduce elapsed time.
	f := &roomClusterFixture{repo: NewRepository(selectionPG(t)), now: time.Now().UTC().Truncate(time.Microsecond), hostConn: new(recordingConn), guestConn: new(recordingConn)}
	room := baseRoom(f.now)
	f.roomID = room.ID
	if _, err := f.repo.CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	makeService := func(user int, profile string) *Service {
		// Drive reconciliation explicitly so tests wait on state, never sleeps.
		s := newServiceForTest(f.now, &stubRepo{room: room}, &stubSessions{session: &playback.Session{UserID: user, ProfileID: profile, MediaFileID: 1}}, &stubFiles{file: &models.MediaFile{ContentID: "movie-1"}}, nil)
		s.repo = NewRepository(f.repo.pool)
		s.now = func() time.Time { return f.now }
		t.Cleanup(s.Close)
		return s
	}
	f.host, f.guest = makeService(7, "host"), makeService(8, "guest")
	var err error
	f.hostReg, _, err = f.host.Connect(t.Context(), room.ID, 7, "host", f.hostConn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.AttachSessionForConnection(t.Context(), f.hostReg, 7, "host", "host-session"); err != nil {
		t.Fatal(err)
	}
	f.guestReg, _, err = f.guest.Connect(t.Context(), room.ID, 8, "guest", f.guestConn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.guest.AttachSessionForConnection(t.Context(), f.guestReg, 8, "guest", "guest-session"); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRoomRuntimeCoordinatesAcrossNodesPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	if err := f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.host.Snapshot(t.Context(), f.roomID, 7, "host")
	if err != nil || snapshot.MemberCount != 2 || len(snapshot.Members) != 2 {
		t.Fatalf("global roster: %+v %v", snapshot, err)
	}
	position := 1500.0
	if _, err = f.host.HandleTransportRequestForConnection(t.Context(), f.hostReg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: &position}); err != nil {
		t.Fatal(err)
	}
	command := lastTransport(t, f.hostConn)
	old := lastTransport(t, f.guestConn)
	// The guest has not received a notification. Its old readiness must still
	// be checked against the new command in the authoritative transaction.
	snapshot, err = f.guest.HandleReadyForConnection(t.Context(), f.guestReg, 8, "guest", StateReport{SessionID: "guest-session", CommandID: old.CommandID, PositionSeconds: position})
	if err != nil || snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("stale ready: %+v %v", snapshot, err)
	}
	if err = f.guest.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	received := lastTransport(t, f.guestConn)
	if received.CommandID != command.CommandID || received.Action != TransportActionSeek || received.SessionID != "guest-session" {
		t.Fatalf("missed event repair: %+v", received)
	}
	snapshot, err = f.host.HandleReadyForConnection(t.Context(), f.hostReg, 7, "host", StateReport{SessionID: "host-session", CommandID: command.CommandID, PositionSeconds: position})
	if err != nil || snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("resumed before remote participant: %+v %v", snapshot, err)
	}
	snapshot, err = f.guest.HandleReadyForConnection(t.Context(), f.guestReg, 8, "guest", StateReport{SessionID: "guest-session", CommandID: command.CommandID, PositionSeconds: position})
	if err != nil || snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("global ready did not resume: %+v %v", snapshot, err)
	}
	if err = f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	if cmd := lastTransport(t, f.hostConn); cmd.Action != TransportActionPlay || cmd.PositionSeconds != position {
		t.Fatalf("remote resume not delivered: %+v", cmd)
	}
}

func TestRoomRuntimeExpiresFailedNodeAndFencesReplacedSocketPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	if _, err := f.host.HandleTransportRequestForConnection(t.Context(), f.hostReg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: new(1500.0)}); err != nil {
		t.Fatal(err)
	}
	command := lastTransport(t, f.hostConn)
	if _, err := f.host.HandleReadyForConnection(t.Context(), f.hostReg, 7, "host", StateReport{SessionID: "host-session", CommandID: command.CommandID, PositionSeconds: 1500}); err != nil {
		t.Fatal(err)
	}
	// Expire the failed server's lease before the barrier deadline. A live
	// connection on this server retains its lease and resumes immediately.
	_, err := f.repo.pool.Exec(t.Context(), `UPDATE watch_together_rooms SET runtime=jsonb_set(runtime, '{members,8:guest,lease_until}', to_jsonb($2::timestamptz)) WHERE id=$1`, f.roomID, f.now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.host.Snapshot(t.Context(), f.roomID, 7, "host")
	if err != nil || snapshot.PlaybackState != RoomPlaybackStatePlaying || snapshot.MemberCount != 1 {
		t.Fatalf("expired member still blocks room: %+v %v", snapshot, err)
	}
	// Reconnect the host through a different API server, then deliver the old
	// server's delayed disconnect and grace-period callback.
	f.guest.sessions = &stubSessions{session: &playback.Session{UserID: 7, ProfileID: "host", MediaFileID: 1}}
	replacement := new(recordingConn)
	reg, _, err := f.guest.Connect(t.Context(), f.roomID, 7, "host", replacement)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.repo.GetRoomByID(t.Context(), f.roomID)
	snapshot, err = f.guest.AttachSessionForConnection(t.Context(), reg, 7, "host", "host-session")
	if err != nil || snapshot.PlaybackState != RoomPlaybackStatePlaying || snapshot.Generation != before.Generation {
		t.Fatalf("cross-node reattach paused room: %+v %v", snapshot, err)
	}
	f.host.Disconnect(f.hostReg, true)
	f.host.closeIfHostStillDisconnected(f.roomID, 7, "host")
	after, err := f.repo.GetRoomByID(t.Context(), f.roomID)
	if err != nil || after.Phase == RoomPhaseEnded {
		t.Fatalf("old host closed replacement connection: %+v %v", after, err)
	}
	if _, err = f.host.HandleTransportRequestForConnection(t.Context(), f.hostReg, 7, "host", TransportRequest{Action: TransportActionPause}); !errors.Is(err, ErrConnectionNotAttached) {
		t.Fatalf("old socket retained authority: %v", err)
	}
}

func TestRoomRuntimeDoesNotDispatchRolledBackCommandsPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	before, _ := f.repo.GetRoomByID(t.Context(), f.roomID)
	frames := len(f.hostConn.payloads)
	failure := errors.New("abort room operation")
	_, err := withRoomOperation(t.Context(), f.host, f.roomID, func(ctx context.Context) (Snapshot, error) {
		if _, err := f.host.handleTransportRequestForConnection(ctx, f.hostReg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: new(1500.0)}); err != nil {
			return Snapshot{}, err
		}
		return Snapshot{}, failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	after, _ := f.repo.GetRoomByID(t.Context(), f.roomID)
	if after.Generation != before.Generation || len(f.hostConn.payloads) != frames {
		t.Fatal("rolled back command escaped transaction")
	}
}

func TestRoomRuntimeRestoresFailedClosePG(t *testing.T) {
	f := newRoomClusterFixture(t)
	before := f.host.rooms[f.roomID]
	frames := len(f.hostConn.payloads)
	// A deferred constraint trigger fails at COMMIT, after CloseRoom has
	// updated local state and queued its socket notifications.
	_, err := f.repo.pool.Exec(t.Context(), `
CREATE FUNCTION reject_room_close() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.phase = 'ended' THEN RAISE EXCEPTION 'injected close failure'; END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER reject_room_close AFTER UPDATE ON watch_together_rooms
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_room_close();`)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.host.CloseRoom(t.Context(), f.roomID, 7, "host"); err == nil {
		t.Fatal("close unexpectedly committed")
	}
	if f.host.rooms[f.roomID] != before || len(f.hostConn.payloads) != frames {
		t.Fatal("failed close lost the live room or sent a close frame")
	}
	room, err := f.repo.GetRoomByID(t.Context(), f.roomID)
	if err != nil || room.Phase == RoomPhaseEnded {
		t.Fatalf("close was not rolled back: %v", err)
	}
	if _, err = f.host.HandleTransportRequestForConnection(t.Context(), f.hostReg, 7, "host", TransportRequest{Action: TransportActionPause}); err != nil {
		t.Fatalf("original socket lost authority after rollback: %v", err)
	}
}

func TestRoomRuntimeRemoteBarrierInvalidatesOldDeadlinePG(t *testing.T) {
	f := newRoomClusterFixture(t)
	if _, err := f.host.HandleTransportRequestForConnection(t.Context(), f.hostReg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: new(100.0)}); err != nil {
		t.Fatal(err)
	}
	oldEpoch := f.host.rooms[f.roomID].waitingEpoch
	f.now = f.now.Add(20 * time.Second)
	f.guest.sessions = &stubSessions{session: &playback.Session{UserID: 7, ProfileID: "host", MediaFileID: 1}}
	reg, _, err := f.guest.Connect(t.Context(), f.roomID, 7, "host", new(recordingConn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.guest.AttachSessionForConnection(t.Context(), reg, 7, "host", "host-session"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.guest.HandleTransportRequestForConnection(t.Context(), reg, 7, "host", TransportRequest{Action: TransportActionPlay}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.guest.HandleTransportRequestForConnection(t.Context(), reg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: new(900.0)}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(11 * time.Second)
	f.host.handleWaitingDeadline(f.roomID, oldEpoch)
	room, err := f.repo.GetRoomByID(t.Context(), f.roomID)
	if err != nil || room.PlaybackState != RoomPlaybackStateWaiting || room.AnchorPositionSeconds != 900 {
		t.Fatalf("stale timer released the new seek: %+v %v", room, err)
	}
	if f.host.rooms[f.roomID].waitingTimer != nil {
		t.Fatal("shared room retained a process-local deadline timer")
	}
	// The guest is ready and the host is not; the deadline skips the host.
	if _, err = f.guest.HandleReadyForConnection(t.Context(), f.guestReg, 8, "guest", StateReport{SessionID: "guest-session", PositionSeconds: 900}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(20 * time.Second)
	if err = f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	room, _ = f.repo.GetRoomByID(t.Context(), f.roomID)
	if room.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatal("current barrier did not expire at its own deadline")
	}
}

func TestRoomRuntimeConcurrentReadyResumesOncePG(t *testing.T) {
	f := newRoomClusterFixture(t)
	if _, err := f.host.HandleTransportRequestForConnection(t.Context(), f.hostReg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: new(1500.0)}); err != nil {
		t.Fatal(err)
	}
	command := lastTransport(t, f.hostConn)
	before, _ := f.repo.GetRoomByID(t.Context(), f.roomID)
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		_, err := f.host.HandleReadyForConnection(t.Context(), f.hostReg, 7, "host", StateReport{SessionID: "host-session", CommandID: command.CommandID, PositionSeconds: 1500})
		done <- err
	}()
	go func() {
		<-start
		_, err := f.guest.HandleReadyForConnection(t.Context(), f.guestReg, 8, "guest", StateReport{SessionID: "guest-session", CommandID: command.CommandID, PositionSeconds: 1500})
		done <- err
	}()
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	after, err := f.repo.GetRoomByID(t.Context(), f.roomID)
	if err != nil || after.Generation != before.Generation+1 || after.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("duplicate or missing resume: %+v %v", after, err)
	}
	if err = f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	frames := len(f.hostConn.payloads)
	if err = f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	if len(f.hostConn.payloads) != frames {
		t.Fatal("unchanged reconciliation broadcast duplicate frames")
	}
}

func TestRoomRuntimeReconnectDuringSeekKeepsDestinationCheckPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	if _, err := f.host.HandleTransportRequestForConnection(t.Context(), f.hostReg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: new(1500.0)}); err != nil {
		t.Fatal(err)
	}
	command := lastTransport(t, f.hostConn)
	if _, err := f.host.HandleReadyForConnection(t.Context(), f.hostReg, 7, "host", StateReport{SessionID: "host-session", CommandID: command.CommandID, PositionSeconds: 1500}); err != nil {
		t.Fatal(err)
	}
	f.host.sessions = &stubSessions{session: &playback.Session{UserID: 8, ProfileID: "guest", MediaFileID: 1}}
	replacement := new(recordingConn)
	reg, _, err := f.host.Connect(t.Context(), f.roomID, 8, "guest", replacement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.AttachSessionForConnection(t.Context(), reg, 8, "guest", "guest-session"); err != nil {
		t.Fatal(err)
	}
	attached := lastTransport(t, replacement)
	if attached.Action != TransportActionSeek || attached.CommandID != command.CommandID {
		t.Fatalf("seek origin lost: %+v", attached)
	}
	snapshot, err := f.host.HandleReadyForConnection(t.Context(), reg, 8, "guest", StateReport{SessionID: "guest-session", CommandID: attached.CommandID, PositionSeconds: 20})
	if err != nil || snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("old media satisfied the seek: %+v %v", snapshot, err)
	}
	snapshot, err = f.host.HandleReadyForConnection(t.Context(), reg, 8, "guest", StateReport{SessionID: "guest-session", CommandID: attached.CommandID, PositionSeconds: 1500})
	if err != nil || snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("current media did not resume: %+v %v", snapshot, err)
	}
}

func TestRoomRuntimeWaitingDeadlineStartsWithAttachmentPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	f.host.selectionResolver = &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie-1"}}
	if _, err := f.host.SelectItem(t.Context(), f.roomID, 7, "host", SelectItemInput{ContentID: "movie-1"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(waitingResumeDeadline + time.Second)
	if err := f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	room, _ := f.repo.GetRoomByID(t.Context(), f.roomID)
	if room.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatal("room resumed before anyone attached playback")
	}
	if _, err := f.host.AttachSessionForConnection(t.Context(), f.hostReg, 7, "host", "host-session"); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(waitingResumeDeadline + time.Second)
	if err := f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	room, _ = f.repo.GetRoomByID(t.Context(), f.roomID)
	if room.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatal("room started without anyone ready to watch")
	}
	// Once someone is ready, the attached straggler no longer holds the room.
	if _, err := f.guest.AttachSessionForConnection(t.Context(), f.guestReg, 8, "guest", "guest-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.guest.HandleReadyForConnection(t.Context(), f.guestReg, 8, "guest", StateReport{SessionID: "guest-session", PositionSeconds: 0}); err != nil {
		t.Fatal(err)
	}
	room, _ = f.repo.GetRoomByID(t.Context(), f.roomID)
	if room.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatal("attached straggler blocked room past the waiting deadline")
	}
}

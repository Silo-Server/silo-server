package watchtogether

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestInvalidPlaybackPositionsAreRejected(t *testing.T) {
	now := time.Now().UTC()
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	conn := &recordingConn{}
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", sessionID: "session", connection: conn}
	reg := registrationFor(repo.room.ID, 7, "host", conn)
	for _, position := range []float64{math.NaN(), math.Inf(1), -1, maxPositionSeconds + 1} {
		p := position
		if _, err := service.HandleTransportRequestForConnection(t.Context(), reg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: &p}); !errors.Is(err, ErrInvalidPosition) {
			t.Fatalf("position %v error = %v", position, err)
		}
		if _, err := service.HandleStateReportForConnection(t.Context(), reg, 7, "host", StateReport{SessionID: "session", PositionSeconds: position}); !errors.Is(err, ErrInvalidPosition) {
			t.Fatalf("state position %v error = %v", position, err)
		}
		if _, err := service.HandleBufferingForConnection(t.Context(), reg, 7, "host", StateReport{SessionID: "session", PositionSeconds: position}); !errors.Is(err, ErrInvalidPosition) {
			t.Fatalf("buffering position %v error = %v", position, err)
		}
		if _, err := service.HandleReadyForConnection(t.Context(), reg, 7, "host", StateReport{SessionID: "session", PositionSeconds: position}); !errors.Is(err, ErrInvalidPosition) {
			t.Fatalf("ready position %v error = %v", position, err)
		}
	}
}

type stubRepo struct {
	room Room
	// anchorErr, when set, is returned from UpdateAnchor to simulate a
	// database failure.
	anchorErr error
}

func (s *stubRepo) CreateRoom(_ context.Context, room Room) (*Room, error) {
	s.room = room
	copy := s.room
	return &copy, nil
}
func (s *stubRepo) GetRoomByID(context.Context, string) (*Room, error) {
	room := s.room
	return &room, nil
}
func (s *stubRepo) GetRoomByCode(context.Context, string) (*Room, error) {
	room := s.room
	return &room, nil
}
func (s *stubRepo) GetRoomByJoinToken(context.Context, string) (*Room, error) {
	room := s.room
	return &room, nil
}
func (s *stubRepo) ListIdleRoomIDs(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}
func (s *stubRepo) UpdatePolicy(_ context.Context, _ string, policy GuestControlPolicy, generation int64, expectedGeneration int64) (*Room, error) {
	if s.room.Generation != expectedGeneration {
		return nil, ErrRoomStateConflict
	}
	s.room.GuestControlPolicy = policy
	s.room.Generation = generation
	room := s.room
	return &room, nil
}
func (s *stubRepo) UpdateAnchor(
	_ context.Context,
	_ string,
	positionSeconds float64,
	isPaused bool,
	playbackState RoomPlaybackState,
	resumeOnReady bool,
	updatedAt time.Time,
	generation int64,
	expectedGeneration int64,
) (*Room, error) {
	if s.anchorErr != nil {
		return nil, s.anchorErr
	}
	if s.room.Generation != expectedGeneration {
		return nil, ErrRoomStateConflict
	}
	s.room.AnchorPositionSeconds = positionSeconds
	s.room.IsPaused = isPaused
	s.room.PlaybackState = playbackState
	s.room.ResumeOnReady = resumeOnReady
	s.room.AnchorUpdatedAt = updatedAt
	s.room.Generation = generation
	room := s.room
	return &room, nil
}
func (s *stubRepo) CloseRoom(_ context.Context, _ string, closedAt time.Time) (*Room, error) {
	s.room.Phase = RoomPhaseEnded
	s.room.ClosedAt = &closedAt
	room := s.room
	return &room, nil
}
func (s *stubRepo) UpdateSelection(
	_ context.Context,
	_ string,
	selection SelectItemInput,
	phase RoomPhase,
	playbackState RoomPlaybackState,
	resumeOnReady bool,
	anchorPosition float64,
	isPaused bool,
	anchorUpdatedAt time.Time,
	selectionRevision int64,
	generation int64,
	expectedGeneration int64,
) (*Room, error) {
	if s.room.Generation != expectedGeneration {
		return nil, ErrRoomStateConflict
	}
	s.room.Phase = phase
	s.room.PlaybackState = playbackState
	s.room.ResumeOnReady = resumeOnReady
	s.room.SelectedContentID = &selection.ContentID
	s.room.SelectedFileID = selection.FileID
	s.room.SelectedLibraryID = selection.LibraryID
	s.room.AnchorPositionSeconds = anchorPosition
	s.room.IsPaused = isPaused
	s.room.AnchorUpdatedAt = anchorUpdatedAt
	s.room.SelectionRevision = selectionRevision
	s.room.Generation = generation
	room := s.room
	return &room, nil
}

func (s *stubRepo) UpdateSelectionMode(
	_ context.Context,
	_ string,
	mode RoomSelectionMode,
	anchorUpdatedAt time.Time,
	generation int64,
	expectedGeneration int64,
) (*Room, error) {
	if s.room.Generation != expectedGeneration || s.room.Phase != RoomPhaseLobby {
		return nil, ErrRoomStateConflict
	}
	s.room.SelectionMode = mode
	s.room.SelectedContentID = nil
	s.room.SelectedFileID = nil
	s.room.SelectedLibraryID = nil
	s.room.AnchorUpdatedAt = anchorUpdatedAt
	s.room.Generation = generation
	room := s.room
	return &room, nil
}

func (s *stubRepo) UpdateStagedSelection(
	_ context.Context,
	_ string,
	selection SelectItemInput,
	anchorUpdatedAt time.Time,
	generation int64,
	expectedGeneration int64,
) (*Room, error) {
	if s.room.Generation != expectedGeneration {
		return nil, ErrRoomStateConflict
	}
	if s.room.Phase != RoomPhaseLobby || s.room.SelectionMode != RoomSelectionModeHostPick {
		return nil, ErrRoomStateConflict
	}
	contentID := selection.ContentID
	s.room.SelectedContentID = &contentID
	s.room.SelectedFileID = selection.FileID
	s.room.SelectedLibraryID = selection.LibraryID
	s.room.AnchorUpdatedAt = anchorUpdatedAt
	s.room.Generation = generation
	room := s.room
	return &room, nil
}

// StartStagedOnce mirrors Repository.StartStagedOnce over the in-memory row.
func (s *stubRepo) StartStagedOnce(_ context.Context, _ string, user int, profile string, expected int64, now time.Time) (*Room, bool, error) {
	if s.room.HostUserID != user || s.room.HostProfileID != profile {
		return nil, false, ErrRoomForbidden
	}
	if s.room.Phase == RoomPhaseEnded {
		return nil, false, ErrRoomClosed
	}
	if s.room.Phase == RoomPhasePlaying || s.room.Generation != expected {
		room := s.room
		return &room, false, nil
	}
	if s.room.SelectedContentID == nil || *s.room.SelectedContentID == "" {
		return nil, false, ErrNoStagedSelection
	}
	s.room.Phase = RoomPhasePlaying
	s.room.PlaybackState = RoomPlaybackStateWaiting
	s.room.ResumeOnReady = true
	s.room.AnchorPositionSeconds = 0
	s.room.IsPaused = true
	s.room.AnchorUpdatedAt = now
	s.room.SelectionRevision++
	s.room.Generation++
	room := s.room
	return &room, true, nil
}

// StopPlaybackOnce mirrors Repository.StopPlaybackOnce over the in-memory row.
func (s *stubRepo) StopPlaybackOnce(_ context.Context, _ string, user int, profile string, expected int64, now time.Time) (*Room, bool, error) {
	if s.room.HostUserID != user || s.room.HostProfileID != profile {
		return nil, false, ErrRoomForbidden
	}
	if s.room.Phase == RoomPhaseEnded {
		return nil, false, ErrRoomClosed
	}
	if s.room.Phase != RoomPhasePlaying || s.room.Generation != expected {
		room := s.room
		return &room, false, nil
	}
	s.room.Phase = RoomPhaseLobby
	s.room.PlaybackState = RoomPlaybackStateIdle
	s.room.ResumeOnReady = false
	s.room.AnchorPositionSeconds = 0
	s.room.IsPaused = true
	s.room.AnchorUpdatedAt = now
	s.room.SelectionRevision++
	s.room.Generation++
	room := s.room
	return &room, true, nil
}

type stubSessions struct {
	session *playback.Session
}

func (s *stubSessions) GetSession(string) (*playback.Session, error) {
	if s.session == nil {
		return nil, playback.ErrSessionNotFound
	}
	cp := *s.session
	return &cp, nil
}

type stubFiles struct {
	file *models.MediaFile
}

func (s *stubFiles) GetByID(context.Context, int) (*models.MediaFile, error) {
	if s.file == nil {
		return nil, errors.New("missing file")
	}
	cp := *s.file
	return &cp, nil
}

type stubConn struct{}

func (stubConn) WriteJSON(any) error { return nil }
func (stubConn) Close() error        { return nil }

type recordingConn struct {
	payloads []map[string]any
}

func (c *recordingConn) WriteJSON(v any) error {
	payload, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	copyPayload := make(map[string]any, len(payload))
	for key, value := range payload {
		copyPayload[key] = value
	}
	c.payloads = append(c.payloads, copyPayload)
	return nil
}

func (c *recordingConn) Close() error { return nil }

type stubSelectionResolver struct {
	resolved *ResolvedSelection
	err      error
}

func (s *stubSelectionResolver) ResolveSelection(context.Context, int, string, SelectItemInput) (*ResolvedSelection, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.resolved, nil
}

func baseRoom(now time.Time) Room {
	return Room{
		ID:                    "room-1",
		Code:                  "ROOM1234",
		JoinToken:             "TOKEN1234",
		HostUserID:            7,
		HostProfileID:         "host",
		Phase:                 RoomPhasePlaying,
		PlaybackState:         RoomPlaybackStatePlaying,
		ResumeOnReady:         false,
		SelectionMode:         RoomSelectionModeHostPick,
		SelectionRevision:     1,
		SelectedContentID:     stringPtr("movie-1"),
		GuestControlPolicy:    GuestControlPolicyHostOnly,
		AnchorPositionSeconds: 10,
		IsPaused:              false,
		AnchorUpdatedAt:       now.Add(-10 * time.Second),
		Generation:            1,
		CreatedAt:             now.Add(-20 * time.Second),
	}
}

func newServiceForTest(now time.Time, repo *stubRepo, sessions *stubSessions, files *stubFiles, resolver WatchTogetherSelectionResolver) *Service {
	service := NewService(repo, sessions, files, resolver, nil, nil)
	service.hostDisconnectTTL = time.Hour
	service.now = func() time.Time { return now }
	service.rooms[repo.room.ID] = &liveRoom{
		room:    repo.room,
		members: make(map[string]*memberState),
	}
	return service
}

func registrationFor(roomID string, userID int, profileID string, conn RoomConnection) *Registration {
	return &Registration{
		roomID:     roomID,
		memberKey:  buildMemberKey(userID, profileID),
		connection: conn,
	}
}

func stringPtr(value string) *string {
	return &value
}

func TestGuestPlayPausePolicyStillRejectsGuestSeek(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.GuestControlPolicy = GuestControlPolicyGuestPlayPause
	conn := &recordingConn{}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = &memberState{
		userID:     8,
		profileID:  "guest",
		sessionID:  "session-1",
		connection: conn,
	}

	position := 120.0
	reg := registrationFor(repo.room.ID, 8, "guest", conn)
	_, err := service.HandleTransportRequestForConnection(context.Background(), reg, 8, "guest", TransportRequest{
		Action:          TransportActionSeek,
		PositionSeconds: &position,
		IsPaused:        false,
	})
	if !errors.Is(err, ErrTransportNotAllowed) {
		t.Fatalf("HandleTransportRequestForConnection(guest seek) error = %v, want ErrTransportNotAllowed", err)
	}
}

func TestGuestDriftTriggersCorrection(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	conn := &recordingConn{}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{
			ID:          "session-1",
			UserID:      8,
			ProfileID:   "guest",
			MediaFileID: 42,
		}},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = &memberState{
		userID:     8,
		profileID:  "guest",
		sessionID:  "session-1",
		connection: conn,
	}

	reg := registrationFor(repo.room.ID, 8, "guest", conn)
	_, err := service.HandleStateReportForConnection(context.Background(), reg, 8, "guest", StateReport{
		SessionID:       "session-1",
		PositionSeconds: 2,
		IsPaused:        false,
	})
	if err != nil {
		t.Fatalf("HandleStateReportForConnection() error = %v", err)
	}

	if len(conn.payloads) == 0 {
		t.Fatal("expected correction commands to be dispatched")
	}
}

func TestGuestDriftCorrectionIsCoalescedAndRearmed(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	current := now
	repo := &stubRepo{room: baseRoom(now)}
	conn := &recordingConn{}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{
			ID:          "session-1",
			UserID:      8,
			ProfileID:   "guest",
			MediaFileID: 42,
		}},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.now = func() time.Time { return current }
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = &memberState{
		userID:     8,
		profileID:  "guest",
		sessionID:  "session-1",
		connection: conn,
	}

	reg := registrationFor(repo.room.ID, 8, "guest", conn)
	report := StateReport{SessionID: "session-1", PositionSeconds: 2}
	if _, err := service.HandleStateReportForConnection(t.Context(), reg, 8, "guest", report); err != nil {
		t.Fatal(err)
	}
	first := lastTransport(t, conn)
	if _, err := service.HandleStateReportForConnection(t.Context(), reg, 8, "guest", report); err != nil {
		t.Fatal(err)
	}
	if got := transportCount(conn); got != 1 {
		t.Fatalf("repeated drift report dispatched %d corrections, want 1", got)
	}

	current = current.Add(guestCorrectionRetryInterval)
	if _, err := service.HandleStateReportForConnection(t.Context(), reg, 8, "guest", report); err != nil {
		t.Fatal(err)
	}
	second := lastTransport(t, conn)
	if got := transportCount(conn); got != 2 {
		t.Fatalf("expired correction dispatched %d corrections, want 2", got)
	}
	if second.CommandID == first.CommandID {
		t.Fatalf("retry reused command ID %q", second.CommandID)
	}

	expected := expectedPosition(repo.room, current)
	if _, err := service.HandleStateReportForConnection(t.Context(), reg, 8, "guest", StateReport{
		SessionID:       "session-1",
		PositionSeconds: expected,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleStateReportForConnection(t.Context(), reg, 8, "guest", report); err != nil {
		t.Fatal(err)
	}
	if got := transportCount(conn); got != 3 {
		t.Fatalf("in-sync report did not re-arm correction, got %d corrections, want 3", got)
	}
}

// transportCount counts the transport commands a connection received,
// ignoring snapshots.
func transportCount(conn *recordingConn) int {
	count := 0
	for _, payload := range conn.payloads {
		if _, ok := payload["command"].(TransportCommand); ok {
			count++
		}
	}
	return count
}

func TestCorrectionCommandRoundTripsRoomRuntime(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	command := &TransportCommand{
		CommandID:         "correction-1",
		SessionID:         "session-1",
		SelectionRevision: repo.room.SelectionRevision,
		Action:            TransportActionPlay,
		PositionSeconds:   20,
		ExecuteAt:         now.Add(time.Second).Format(time.RFC3339Nano),
		IssuedAt:          now.Format(time.RFC3339Nano),
		PlaybackState:     RoomPlaybackStatePlaying,
	}
	key := buildMemberKey(8, "guest")
	live := service.rooms[repo.room.ID]
	live.members[key] = &memberState{
		userID:            8,
		profileID:         "guest",
		sessionID:         command.SessionID,
		correctionCommand: command,
		syncingToRoom:     true,
		lobbyReady:        true,
	}

	encoded, err := json.Marshal(service.runtimeLocked(live))
	if err != nil {
		t.Fatal(err)
	}
	var state roomRuntime
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	if state.Members[key].CorrectionCommand == nil {
		t.Fatal("correction command was omitted from room runtime")
	}
	if !state.Members[key].SyncingToRoom || !state.Members[key].LobbyReady {
		t.Fatal("correction persistence dropped the other member runtime fields")
	}

	adopted := &liveRoom{members: make(map[string]*memberState)}
	service.adoptRuntimeLocked(t.Context(), adopted, repo.room, state)
	got := adopted.members[key].correctionCommand
	if got == nil || got.CommandID != command.CommandID || got.SessionID != command.SessionID {
		t.Fatalf("adopted correction command = %+v, want %+v", got, command)
	}
	if !adopted.members[key].syncingToRoom || !adopted.members[key].lobbyReady {
		t.Fatal("runtime adoption dropped syncing or lobby readiness")
	}

	nextRoom := repo.room
	nextRoom.SelectionRevision++
	service.adoptRuntimeLocked(t.Context(), adopted, nextRoom, state)
	if member := adopted.members[key]; member.correctionCommand != nil || member.syncingToRoom || member.lobbyReady {
		t.Fatalf("previous epoch state survived runtime adoption: %+v", member)
	}
}

func TestCorrectionCommandPendingAllowsBoundedFutureClockSkew(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	command := TransportCommand{
		SessionID:         "session-1",
		SelectionRevision: 1,
		Action:            TransportActionPlay,
		PlaybackState:     RoomPlaybackStatePlaying,
	}
	member := &memberState{correctionCommand: &command}

	for _, test := range []struct {
		name      string
		issuedAt  time.Time
		wantMatch bool
	}{
		{name: "small future skew", issuedAt: now.Add(time.Second), wantMatch: true},
		{name: "retry interval elapsed", issuedAt: now.Add(-guestCorrectionRetryInterval), wantMatch: false},
		{name: "excessive future skew", issuedAt: now.Add(guestCorrectionRetryInterval + time.Millisecond), wantMatch: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			member.correctionCommand.IssuedAt = test.issuedAt.Format(time.RFC3339Nano)
			if got := correctionCommandPending(member, command, now); got != test.wantMatch {
				t.Fatalf("correctionCommandPending() = %t, want %t", got, test.wantMatch)
			}
		})
	}
}

func TestStateReportsPreservePendingSeek(t *testing.T) {
	for _, seekPaused := range []bool{false, true} {
		name := "playing"
		if seekPaused {
			name = "paused"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 9, 20, 15, 0, 0, 0, time.UTC)
			repo := &stubRepo{room: baseRoom(now)}
			service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
			t.Cleanup(service.Close)
			members := []struct {
				userID    int
				profileID string
				sessionID string
				conn      *recordingConn
			}{
				{7, "host", "host-session", &recordingConn{}},
				{8, "guest", "guest-session", &recordingConn{}},
			}
			for _, member := range members {
				service.rooms[repo.room.ID].members[buildMemberKey(member.userID, member.profileID)] = &memberState{
					userID: member.userID, profileID: member.profileID,
					sessionID: member.sessionID, connection: member.conn,
				}
			}
			const target = 1500.0
			hostReg := registrationFor(repo.room.ID, 7, "host", members[0].conn)
			seek, err := service.HandleTransportRequestForConnection(t.Context(), hostReg, 7, "host", TransportRequest{
				Action: TransportActionSeek, PositionSeconds: new(target), IsPaused: seekPaused,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range members {
				member.conn.payloads = nil
			}

			// A stream rebuild can outlast several periodic reports. Until it
			// lands, both playing and paused reports still carry the old position.
			for _, isPaused := range []bool{false, true} {
				for _, member := range members {
					reg := registrationFor(repo.room.ID, member.userID, member.profileID, member.conn)
					snapshot, err := service.HandleStateReportForConnection(t.Context(), reg, member.userID, member.profileID, StateReport{
						SessionID: member.sessionID, PositionSeconds: 20, IsPaused: isPaused,
					})
					if err != nil {
						t.Fatal(err)
					}
					if snapshot.AnchorPositionSeconds != target || !snapshot.IsPaused || snapshot.PlaybackState != RoomPlaybackStateWaiting {
						t.Fatalf("%s report changed pending seek: position=%v paused=%v state=%s", member.profileID, snapshot.AnchorPositionSeconds, snapshot.IsPaused, snapshot.PlaybackState)
					}
					if repo.room.Generation != seek.Generation {
						t.Fatal("state report persisted a change while the seek was loading")
					}
					if len(member.conn.payloads) != 0 {
						t.Fatal("state report dispatched a correction while the seek was loading")
					}
				}
			}

			for _, member := range members {
				reg := registrationFor(repo.room.ID, member.userID, member.profileID, member.conn)
				if _, err := service.HandleReadyForConnection(t.Context(), reg, member.userID, member.profileID, StateReport{
					SessionID: member.sessionID, PositionSeconds: target, IsPaused: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			wantAction := TransportActionPlay
			if seekPaused {
				wantAction = TransportActionPause
			}
			for _, member := range members {
				last := member.conn.payloads[len(member.conn.payloads)-1]
				command, ok := last["command"].(TransportCommand)
				if !ok || command.Action != wantAction || command.PositionSeconds != target {
					t.Fatalf("%s resume command = %+v, want %s at %v", member.profileID, last, wantAction, target)
				}
			}
		})
	}
}

func TestHostAttachKeepsRoomSelectionAnchor(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.AnchorPositionSeconds = 0
	repo.room.IsPaused = true
	repo.room.AnchorUpdatedAt = now
	repo.room.Generation = 1
	conn := &recordingConn{}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{
			ID:          "session-1",
			UserID:      7,
			ProfileID:   "host",
			MediaFileID: 42,
			Position:    318,
			IsPaused:    false,
		}},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID:     7,
		profileID:  "host",
		connection: conn,
	}

	reg := registrationFor(repo.room.ID, 7, "host", conn)
	snapshot, err := service.AttachSessionForConnection(context.Background(), reg, 7, "host", "session-1")
	if err != nil {
		t.Fatalf("AttachSessionForConnection() error = %v", err)
	}

	if snapshot.AnchorPositionSeconds != 0 {
		t.Fatalf("snapshot anchor = %v, want 0", snapshot.AnchorPositionSeconds)
	}
	if !snapshot.IsPaused {
		t.Fatal("snapshot should remain paused")
	}
	if repo.room.Generation != 1 {
		t.Fatalf("generation = %d, want 1", repo.room.Generation)
	}
	if len(conn.payloads) == 0 {
		t.Fatal("expected room sync commands to be dispatched")
	}
}

func TestHostAttachKeepsRoomSelectionAnchorEvenWhenGuestAttached(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.AnchorPositionSeconds = 0
	repo.room.IsPaused = true
	repo.room.AnchorUpdatedAt = now
	repo.room.Generation = 1
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{
			ID:          "host-session",
			UserID:      7,
			ProfileID:   "host",
			MediaFileID: 42,
			Position:    318,
			IsPaused:    false,
		}},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = &memberState{
		userID:     8,
		profileID:  "guest",
		sessionID:  "guest-session",
		connection: stubConn{},
	}
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID:     7,
		profileID:  "host",
		connection: stubConn{},
	}

	reg := registrationFor(repo.room.ID, 7, "host", stubConn{})
	snapshot, err := service.AttachSessionForConnection(context.Background(), reg, 7, "host", "host-session")
	if err != nil {
		t.Fatalf("AttachSessionForConnection() error = %v", err)
	}

	if snapshot.AnchorPositionSeconds != 0 {
		t.Fatalf("snapshot anchor = %v, want 0", snapshot.AnchorPositionSeconds)
	}
	if !snapshot.IsPaused {
		t.Fatal("snapshot should remain paused")
	}
}

func TestAttachSessionAcceptsEpisodeContentID(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.SelectedContentID = stringPtr("episode-19")
	repo.room.AnchorPositionSeconds = 0
	repo.room.IsPaused = true
	repo.room.AnchorUpdatedAt = now
	repo.room.Generation = 1
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{
			ID:          "host-session",
			UserID:      7,
			ProfileID:   "host",
			MediaFileID: 42,
			Position:    75,
			IsPaused:    false,
		}},
		&stubFiles{file: &models.MediaFile{
			ID:        42,
			ContentID: "series-1",
			EpisodeID: "episode-19",
		}},
		nil,
	)
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID:     7,
		profileID:  "host",
		connection: stubConn{},
	}

	reg := registrationFor(repo.room.ID, 7, "host", stubConn{})
	snapshot, err := service.AttachSessionForConnection(context.Background(), reg, 7, "host", "host-session")
	if err != nil {
		t.Fatalf("AttachSessionForConnection() error = %v", err)
	}

	if snapshot.AttachedSessionID != "host-session" {
		t.Fatalf("attached session = %q, want host-session", snapshot.AttachedSessionID)
	}
	if snapshot.AnchorPositionSeconds != 0 {
		t.Fatalf("snapshot anchor = %v, want 0", snapshot.AnchorPositionSeconds)
	}
}

func TestCreateRoomStartsInLobbyWithoutSelection(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{}
	service := NewService(repo, &stubSessions{}, &stubFiles{}, nil, nil, nil)
	service.now = func() time.Time { return now }

	room, err := service.CreateRoom(context.Background(), CreateRoomInput{
		HostUserID:    7,
		HostProfileID: "host",
	})
	if err != nil {
		t.Fatalf("CreateRoom() error = %v", err)
	}

	if room.Phase != RoomPhaseLobby {
		t.Fatalf("phase = %q, want %q", room.Phase, RoomPhaseLobby)
	}
	if room.SelectionRevision != 0 {
		t.Fatalf("selection revision = %d, want 0", room.SelectionRevision)
	}
	if room.SelectedContentID != nil {
		t.Fatalf("selected content = %v, want nil", *room.SelectedContentID)
	}
}

func TestHostCanSelectItemFromLobby(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: Room{
		ID:                 "room-1",
		Code:               "ROOM1234",
		JoinToken:          "TOKEN1234",
		HostUserID:         7,
		HostProfileID:      "host",
		Phase:              RoomPhaseLobby,
		SelectionMode:      RoomSelectionModeHostPick,
		GuestControlPolicy: GuestControlPolicyHostOnly,
		IsPaused:           true,
		AnchorUpdatedAt:    now,
		Generation:         1,
		CreatedAt:          now,
	}}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{},
		&stubFiles{},
		&stubSelectionResolver{resolved: &ResolvedSelection{
			ContentID: "movie-2",
			FileID:    intPtr(55),
			LibraryID: intPtr(6),
		}},
	)

	snapshot, err := service.SelectItem(context.Background(), "room-1", 7, "host", SelectItemInput{
		ContentID: "movie-2",
	})
	if err != nil {
		t.Fatalf("SelectItem() error = %v", err)
	}

	if snapshot.Phase != RoomPhasePlaying {
		t.Fatalf("phase = %q, want %q", snapshot.Phase, RoomPhasePlaying)
	}
	if snapshot.SelectionRevision != 1 {
		t.Fatalf("selection revision = %d, want 1", snapshot.SelectionRevision)
	}
	if snapshot.SelectedContentID == nil || *snapshot.SelectedContentID != "movie-2" {
		t.Fatalf("selected content = %v, want movie-2", snapshot.SelectedContentID)
	}
	if snapshot.AnchorPositionSeconds != 0 {
		t.Fatalf("anchor = %v, want 0", snapshot.AnchorPositionSeconds)
	}
	if !snapshot.IsPaused {
		t.Fatal("room should stay paused while waiting for participants to get ready")
	}
	if snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("playback state = %q, want %q", snapshot.PlaybackState, RoomPlaybackStateWaiting)
	}
}

func TestSelectItemClearsStaleMemberSessions(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{},
		&stubFiles{},
		&stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie-2"}},
	)
	guest := &memberState{
		userID:     8,
		profileID:  "guest",
		sessionID:  "old-session",
		isReady:    true,
		ignoreWait: true,
		connection: stubConn{},
	}
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = guest

	_, err := service.SelectItem(context.Background(), "room-1", 7, "host", SelectItemInput{
		ContentID: "movie-2",
	})
	if err != nil {
		t.Fatalf("SelectItem() error = %v", err)
	}

	if guest.sessionID != "" {
		t.Fatalf("guest session = %q, want cleared", guest.sessionID)
	}
	if guest.isReady || guest.ignoreWait {
		t.Fatal("guest readiness flags should reset on new selection")
	}
}

func TestGuestCannotSelectItem(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{},
		&stubFiles{},
		&stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie-2"}},
	)

	_, err := service.SelectItem(context.Background(), "room-1", 8, "guest", SelectItemInput{
		ContentID: "movie-2",
	})
	if !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("SelectItem() error = %v, want ErrRoomForbidden", err)
	}
}

func TestSelectItemRejectsInvalidSelection(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{},
		&stubFiles{},
		&stubSelectionResolver{err: ErrInvalidSelection},
	)

	_, err := service.SelectItem(context.Background(), "room-1", 7, "host", SelectItemInput{
		ContentID: "series-1",
	})
	if !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("SelectItem() error = %v, want ErrInvalidSelection", err)
	}
}

func TestAttachSessionEnforcesSelectedFileID(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.SelectedFileID = intPtr(99)
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{
			ID:          "host-session",
			UserID:      7,
			ProfileID:   "host",
			MediaFileID: 42,
		}},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID:     7,
		profileID:  "host",
		connection: stubConn{},
	}

	reg := registrationFor(repo.room.ID, 7, "host", stubConn{})
	_, err := service.AttachSessionForConnection(context.Background(), reg, 7, "host", "host-session")
	if !errors.Is(err, ErrSessionMismatch) {
		t.Fatalf("AttachSessionForConnection() error = %v, want ErrSessionMismatch", err)
	}
}

func TestDisconnectOfLastUnreadyMemberResumesWaitingRoom(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.PlaybackState = RoomPlaybackStateWaiting
	repo.room.IsPaused = true
	repo.room.ResumeOnReady = true
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)

	hostConn := &recordingConn{}
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID:     7,
		profileID:  "host",
		sessionID:  "host-session",
		isReady:    true,
		connection: hostConn,
	}
	guestConn := &recordingConn{}
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = &memberState{
		userID:     8,
		profileID:  "guest",
		sessionID:  "guest-session",
		isReady:    false,
		connection: guestConn,
	}

	service.Disconnect(registrationFor(repo.room.ID, 8, "guest", guestConn), false)

	if repo.room.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("playback state = %q, want %q", repo.room.PlaybackState, RoomPlaybackStatePlaying)
	}
	foundCommand := false
	for _, payload := range hostConn.payloads {
		if payload["type"] == "transport_command" {
			foundCommand = true
		}
	}
	if !foundCommand {
		t.Fatal("expected a resume transport command for the remaining member")
	}
}

func TestBufferingReportCannotTeleportRoomAnchor(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	guestConn := &recordingConn{}
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = &memberState{
		userID:     8,
		profileID:  "guest",
		sessionID:  "guest-session",
		connection: guestConn,
	}

	// Anchor was 10s, 10s ago and playing: expected position is ~20s. A
	// report claiming 500s must be clamped back to the expected position.
	reg := registrationFor(repo.room.ID, 8, "guest", guestConn)
	snapshot, err := service.HandleBufferingForConnection(context.Background(), reg, 8, "guest", StateReport{
		SessionID:       "guest-session",
		PositionSeconds: 500,
		IsPaused:        false,
	})
	if err != nil {
		t.Fatalf("HandleBufferingForConnection() error = %v", err)
	}
	if snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("playback state = %q, want %q", snapshot.PlaybackState, RoomPlaybackStateWaiting)
	}
	if snapshot.AnchorPositionSeconds > 20.001 || snapshot.AnchorPositionSeconds < 19.999 {
		t.Fatalf("anchor = %v, want ~20 (clamped)", snapshot.AnchorPositionSeconds)
	}
}

func TestPingIsClampedToMaxTransportLead(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	guestConn := &recordingConn{}
	member := &memberState{
		userID:     8,
		profileID:  "guest",
		sessionID:  "guest-session",
		connection: guestConn,
	}
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = member

	reg := registrationFor(repo.room.ID, 8, "guest", guestConn)
	if err := service.HandlePingForConnection(context.Background(), reg, 8, "guest", 3_600_000); err != nil {
		t.Fatalf("HandlePingForConnection() error = %v", err)
	}
	if member.lastPingMS != maxTransportLead.Milliseconds() {
		t.Fatalf("lastPingMS = %d, want %d", member.lastPingMS, maxTransportLead.Milliseconds())
	}
}

func TestReadyPersistFailureKeepsWaitingState(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.PlaybackState = RoomPlaybackStateWaiting
	repo.room.IsPaused = true
	repo.room.ResumeOnReady = true
	repo.anchorErr = errors.New("database unavailable")
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)

	hostConn := &recordingConn{}
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID:     7,
		profileID:  "host",
		sessionID:  "host-session",
		connection: hostConn,
	}

	reg := registrationFor(repo.room.ID, 7, "host", hostConn)
	snapshot, err := service.HandleReadyForConnection(context.Background(), reg, 7, "host", StateReport{
		SessionID: "host-session",
	})
	if err != nil {
		t.Fatalf("HandleReadyForConnection() error = %v", err)
	}

	if snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("playback state = %q, want %q (resume must not be announced when persistence failed)",
			snapshot.PlaybackState, RoomPlaybackStateWaiting)
	}
	live := service.rooms[repo.room.ID]
	if live.room.Generation != repo.room.Generation {
		t.Fatalf("live generation = %d, want %d (failed write must not leave a phantom generation)",
			live.room.Generation, repo.room.Generation)
	}
	if live.waitingTimer == nil {
		t.Fatal("waiting deadline should stay armed so the resume is retried")
	}
}

func TestStaleLiveConflictAdoptsDatabaseRow(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	// The database row has moved ahead of the cached live copy.
	repo.room.Generation = 5

	hostConn := &recordingConn{}
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID:     7,
		profileID:  "host",
		sessionID:  "host-session",
		connection: hostConn,
	}

	reg := registrationFor(repo.room.ID, 7, "host", hostConn)
	snapshot, err := service.HandleTransportRequestForConnection(context.Background(), reg, 7, "host", TransportRequest{
		Action: TransportActionPause,
	})
	if err != nil {
		t.Fatalf("HandleTransportRequestForConnection() error = %v", err)
	}

	if snapshot.Generation != 5 {
		t.Fatalf("snapshot generation = %d, want 5 (conflict must adopt the newer database row)", snapshot.Generation)
	}
}

func TestStateReportConflictClearsStaleCorrectionCommands(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)

	guestCorrection := &TransportCommand{
		CommandID:         "stale-correction",
		SessionID:         "guest-session",
		SelectionRevision: repo.room.SelectionRevision,
		Action:            TransportActionPlay,
		PositionSeconds:   20,
		IssuedAt:          now.Add(-time.Second).Format(time.RFC3339Nano),
		PlaybackState:     RoomPlaybackStatePlaying,
	}
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = &memberState{
		userID:            8,
		profileID:         "guest",
		sessionID:         "guest-session",
		correctionCommand: guestCorrection,
	}
	hostConn := &recordingConn{}
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{
		userID:     7,
		profileID:  "host",
		sessionID:  "host-session",
		connection: hostConn,
	}

	// The database row advances independently and wins the host's stale write.
	repo.room.Generation = 5
	repo.room.AnchorPositionSeconds = 40
	repo.room.AnchorUpdatedAt = now

	snapshot, err := service.HandleStateReportForConnection(
		context.Background(),
		registrationFor(repo.room.ID, 7, "host", hostConn),
		7,
		"host",
		StateReport{SessionID: "host-session", PositionSeconds: 30},
	)
	if err != nil {
		t.Fatalf("HandleStateReportForConnection() error = %v", err)
	}
	if snapshot.Generation != repo.room.Generation || snapshot.AnchorPositionSeconds != repo.room.AnchorPositionSeconds {
		t.Fatalf("snapshot = generation %d, anchor %v; want winning row generation %d, anchor %v",
			snapshot.Generation, snapshot.AnchorPositionSeconds, repo.room.Generation, repo.room.AnchorPositionSeconds)
	}
	if service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")].correctionCommand != nil {
		t.Fatal("stale guest correction survived a conflicting host anchor update")
	}
}

func intPtr(value int) *int {
	return &value
}

func TestReadyAcknowledgesCurrentSeek(t *testing.T) {
	for _, test := range []struct {
		name         string
		staleCommand bool
		legacy       bool
		position     float64
	}{
		{"old command at current position", true, false, 1500},
		{"current command at old position", false, false, 20},
		{"legacy report at old position", false, true, 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			repo := &stubRepo{room: baseRoom(now)}
			service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
			live := service.rooms[repo.room.ID]
			t.Cleanup(func() {
				service.mu.Lock()
				service.disarmWaitingDeadlineLocked(live)
				service.mu.Unlock()
				service.Close()
			})
			conn := &recordingConn{}
			member := &memberState{userID: 7, profileID: "host", sessionID: "session", connection: conn}
			live.members[buildMemberKey(7, "host")] = member
			reg := registrationFor(repo.room.ID, 7, "host", conn)
			seek := func() TransportCommand {
				t.Helper()
				_, err := service.HandleTransportRequestForConnection(t.Context(), reg, 7, "host", TransportRequest{
					Action: TransportActionSeek, PositionSeconds: new(1500.0),
				})
				if err != nil {
					t.Fatal(err)
				}
				return conn.payloads[len(conn.payloads)-1]["command"].(TransportCommand)
			}
			previous := seek()
			current := seek()
			commandID := current.CommandID
			if test.staleCommand {
				commandID = previous.CommandID
			}
			if test.legacy {
				commandID = ""
			}
			conn.payloads = nil
			generation := repo.room.Generation
			snapshot, err := service.HandleReadyForConnection(t.Context(), reg, 7, "host", StateReport{
				SessionID: "session", CommandID: commandID, PositionSeconds: test.position, IsPaused: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.PlaybackState != RoomPlaybackStateWaiting || member.isReady || repo.room.Generation != generation || len(conn.payloads) != 0 {
				t.Fatalf("stale ready changed the waiting room: state=%s ready=%v generation=%d frames=%d", snapshot.PlaybackState, member.isReady, repo.room.Generation, len(conn.payloads))
			}
			commandID = current.CommandID
			if test.legacy {
				commandID = ""
			}
			snapshot, err = service.HandleReadyForConnection(t.Context(), reg, 7, "host", StateReport{
				SessionID: "session", CommandID: commandID, PositionSeconds: 1500.5, IsPaused: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.PlaybackState != RoomPlaybackStatePlaying || snapshot.AnchorPositionSeconds != 1500 {
				t.Fatalf("current ready did not resume at the seek target: %+v", snapshot)
			}
		})
	}
}

// A host who rejoins a playing room alone is told the room's position. Their
// stream starts at zero, and the first state report can arrive before that
// seek lands; it must not become the new anchor.
func TestHostRejoinReportsDoNotRewindTheRoomUntilSynced(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.AnchorPositionSeconds = 600
	repo.room.IsPaused = false
	repo.room.AnchorUpdatedAt = now
	conn := &recordingConn{}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{ID: "session-1", UserID: 7, ProfileID: "host", MediaFileID: 42}},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", connection: conn}
	reg := registrationFor(repo.room.ID, 7, "host", conn)
	if _, err := service.AttachSessionForConnection(context.Background(), reg, 7, "host", "session-1"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if len(conn.payloads) == 0 {
		t.Fatal("expected a sync command for the rejoining host")
	}
	before := len(conn.payloads)

	// Stream just started: position 0 while the room is at ~600.
	if _, err := service.HandleStateReportForConnection(context.Background(), reg, 7, "host", StateReport{SessionID: "session-1", PositionSeconds: 0, IsPaused: false}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if repo.room.AnchorPositionSeconds != 600 {
		t.Fatalf("host's pre-seek report rewound the room to %v", repo.room.AnchorPositionSeconds)
	}
	if len(conn.payloads) == before {
		t.Fatal("expected the unsynced host to be corrected toward the room like a guest")
	}
	corrected := len(conn.payloads)
	if _, err := service.HandleStateReportForConnection(t.Context(), reg, 7, "host", StateReport{SessionID: "session-1", PositionSeconds: 0, IsPaused: false}); err != nil {
		t.Fatalf("repeated unsynced report: %v", err)
	}
	if len(conn.payloads) != corrected || repo.room.AnchorPositionSeconds != 600 || !service.rooms[repo.room.ID].members[buildMemberKey(7, "host")].syncingToRoom {
		t.Fatal("repeated host sync report resent correction or restored authority before convergence")
	}

	// The seek landed: the host now reports the room's position and becomes authoritative again.
	if _, err := service.HandleStateReportForConnection(context.Background(), reg, 7, "host", StateReport{SessionID: "session-1", PositionSeconds: 600.4, IsPaused: false}); err != nil {
		t.Fatalf("synced report: %v", err)
	}
	if _, err := service.HandleStateReportForConnection(context.Background(), reg, 7, "host", StateReport{SessionID: "session-1", PositionSeconds: 650, IsPaused: false}); err != nil {
		t.Fatalf("authoritative report: %v", err)
	}
	if repo.room.AnchorPositionSeconds != 650 {
		t.Fatalf("host authority not restored after sync: anchor=%v", repo.room.AnchorPositionSeconds)
	}
}

// An explicit host action while still syncing is intent, not a stale report.
func TestHostTransportRequestEndsRejoinSync(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.AnchorPositionSeconds = 600
	repo.room.IsPaused = false
	repo.room.AnchorUpdatedAt = now
	conn := &recordingConn{}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{ID: "session-1", UserID: 7, ProfileID: "host", MediaFileID: 42}},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", connection: conn}
	reg := registrationFor(repo.room.ID, 7, "host", conn)
	if _, err := service.AttachSessionForConnection(context.Background(), reg, 7, "host", "session-1"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	position := 30.0
	if _, err := service.HandleTransportRequestForConnection(context.Background(), reg, 7, "host", TransportRequest{Action: TransportActionSeek, PositionSeconds: &position, IsPaused: false}); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if repo.room.AnchorPositionSeconds != 30 {
		t.Fatalf("host seek ignored: anchor=%v", repo.room.AnchorPositionSeconds)
	}
	if service.rooms[repo.room.ID].members[buildMemberKey(7, "host")].syncingToRoom {
		t.Fatal("explicit transport should end the rejoin sync")
	}
}

// A rebuilt stream lands the host on a keyframe or segment boundary short of
// the seek target. The host's real position becomes the anchor and the room
// resumes instead of waiting out the deadline; a guest at the same offset is
// still held to the destination.
func TestHostReadyNearSeekTargetReanchorsTheRoom(t *testing.T) {
	now := time.Now().UTC()
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	live := service.rooms[repo.room.ID]
	t.Cleanup(func() {
		service.mu.Lock()
		service.disarmWaitingDeadlineLocked(live)
		service.mu.Unlock()
		service.Close()
	})
	hostConn := &recordingConn{}
	guestConn := &recordingConn{}
	live.members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", sessionID: "host-session", connection: hostConn}
	live.members[buildMemberKey(8, "guest")] = &memberState{userID: 8, profileID: "guest", sessionID: "guest-session", connection: guestConn}
	hostReg := registrationFor(repo.room.ID, 7, "host", hostConn)
	guestReg := registrationFor(repo.room.ID, 8, "guest", guestConn)
	if _, err := service.HandleTransportRequestForConnection(t.Context(), hostReg, 7, "host", TransportRequest{
		Action: TransportActionSeek, PositionSeconds: new(1500.0),
	}); err != nil {
		t.Fatal(err)
	}
	command := hostConn.payloads[len(hostConn.payloads)-1]["command"].(TransportCommand)

	snapshot, err := service.HandleReadyForConnection(t.Context(), guestReg, 8, "guest", StateReport{
		SessionID: "guest-session", CommandID: command.CommandID, PositionSeconds: 1494, IsPaused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlaybackState != RoomPlaybackStateWaiting || live.members[buildMemberKey(8, "guest")].isReady {
		t.Fatalf("guest short of the seek target was accepted: %+v", snapshot)
	}

	snapshot, err = service.HandleReadyForConnection(t.Context(), hostReg, 7, "host", StateReport{
		SessionID: "host-session", CommandID: command.CommandID, PositionSeconds: 1494, IsPaused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !live.members[buildMemberKey(7, "host")].isReady || snapshot.AnchorPositionSeconds != 1494 {
		t.Fatalf("host ready near the target did not re-anchor: %+v", snapshot)
	}
	if snapshot.PlaybackState != RoomPlaybackStateWaiting {
		t.Fatalf("room resumed before the guest was ready: %+v", snapshot)
	}

	snapshot, err = service.HandleReadyForConnection(t.Context(), hostReg, 7, "host", StateReport{
		SessionID: "host-session", CommandID: command.CommandID, PositionSeconds: 1400, IsPaused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AnchorPositionSeconds != 1494 {
		t.Fatalf("host far from the target moved the anchor: %+v", snapshot)
	}
}

// A state report carrying is_ready is the periodic form of the ready message:
// a lost or rejected acknowledgement heals on the next tick without a new
// media event.
func TestStateReportWithIsReadyAcknowledgesTheWaitingCommand(t *testing.T) {
	now := time.Now().UTC()
	repo := &stubRepo{room: baseRoom(now)}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	live := service.rooms[repo.room.ID]
	t.Cleanup(func() {
		service.mu.Lock()
		service.disarmWaitingDeadlineLocked(live)
		service.mu.Unlock()
		service.Close()
	})
	conn := &recordingConn{}
	member := &memberState{userID: 7, profileID: "host", sessionID: "session", connection: conn}
	live.members[buildMemberKey(7, "host")] = member
	reg := registrationFor(repo.room.ID, 7, "host", conn)
	if _, err := service.HandleTransportRequestForConnection(t.Context(), reg, 7, "host", TransportRequest{
		Action: TransportActionSeek, PositionSeconds: new(1500.0),
	}); err != nil {
		t.Fatal(err)
	}
	command := conn.payloads[len(conn.payloads)-1]["command"].(TransportCommand)

	snapshot, err := service.HandleStateReportForConnection(t.Context(), reg, 7, "host", StateReport{
		SessionID: "session", PositionSeconds: 1500, IsPaused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlaybackState != RoomPlaybackStateWaiting || member.isReady {
		t.Fatalf("plain state report satisfied the barrier: %+v", snapshot)
	}
	snapshot, err = service.HandleStateReportForConnection(t.Context(), reg, 7, "host", StateReport{
		SessionID: "session", CommandID: "stale", PositionSeconds: 1500, IsPaused: true, IsReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlaybackState != RoomPlaybackStateWaiting || member.isReady {
		t.Fatalf("stale ready report satisfied the barrier: %+v", snapshot)
	}
	snapshot, err = service.HandleStateReportForConnection(t.Context(), reg, 7, "host", StateReport{
		SessionID: "session", CommandID: command.CommandID, PositionSeconds: 1500, IsPaused: true, IsReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlaybackState != RoomPlaybackStatePlaying || snapshot.AnchorPositionSeconds != 1500 {
		t.Fatalf("ready report did not resume the room: %+v", snapshot)
	}
}

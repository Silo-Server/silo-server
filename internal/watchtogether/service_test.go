package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

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

func intPtr(value int) *int {
	return &value
}

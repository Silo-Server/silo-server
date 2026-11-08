package watchtogether

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func (s *stubRepo) UpdateVisibility(_ context.Context, _ string, visibility RoomVisibility, generation int64, expectedGeneration int64) (*Room, error) {
	if s.room.Generation != expectedGeneration {
		return nil, ErrRoomStateConflict
	}
	s.room.Visibility = visibility
	s.room.Generation = generation
	room := s.room
	return &room, nil
}

// fakeJoinRequests keeps requests in memory and lists the stub repo's room.
type fakeJoinRequests struct {
	repo     *stubRepo
	runtime  []byte
	requests map[string]*JoinRequest
}

func (f *fakeJoinRequests) ListPublicRooms(_ context.Context, user int, profile string, _ int) ([]publicRoomRow, error) {
	if f.repo.room.visibility() != RoomVisibilityPublic || f.repo.room.Phase == RoomPhaseEnded {
		return nil, nil
	}
	row := publicRoomRow{Room: f.repo.room, Runtime: f.runtime}
	for _, request := range f.requests {
		if request.UserID == user && request.ProfileID == profile {
			out := *request
			row.Request = &out
		}
	}
	return []publicRoomRow{row}, nil
}

func (f *fakeJoinRequests) CreateJoinRequest(_ context.Context, request JoinRequest) (*JoinRequest, bool, error) {
	for _, existing := range f.requests {
		if existing.RoomID == request.RoomID && existing.UserID == request.UserID && existing.ProfileID == request.ProfileID {
			if existing.Status != JoinRequestStatusDenied {
				out := *existing
				return &out, false, nil
			}
			existing.Status, existing.DecidedAt = JoinRequestStatusPending, nil
			out := *existing
			return &out, true, nil
		}
	}
	f.requests[request.ID] = &request
	out := request
	return &out, true, nil
}

func (f *fakeJoinRequests) GetJoinRequest(_ context.Context, id string) (*JoinRequest, error) {
	request := f.requests[id]
	if request == nil {
		return nil, ErrJoinRequestNotFound
	}
	out := *request
	return &out, nil
}

func (f *fakeJoinRequests) ListPendingJoinRequests(_ context.Context, roomID string, _ int) ([]JoinRequest, error) {
	out := []JoinRequest{}
	for _, request := range f.requests {
		if request.RoomID == roomID && request.Status == JoinRequestStatusPending {
			out = append(out, *request)
		}
	}
	return out, nil
}

func (f *fakeJoinRequests) DecideJoinRequest(_ context.Context, roomID, id string, status JoinRequestStatus, at time.Time) (*JoinRequest, error) {
	request := f.requests[id]
	if request == nil || request.RoomID != roomID {
		return nil, ErrJoinRequestNotFound
	}
	if request.Status == JoinRequestStatusApproved && status == JoinRequestStatusDenied {
		return nil, ErrJoinRequestApproved
	}
	request.Status, request.DecidedAt = status, &at
	out := *request
	return &out, nil
}

func newPublicRoomsServiceForTest(t *testing.T) (*Service, *stubRepo, *fakeJoinRequests, *recordingConn) {
	t.Helper()
	now := time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	repo.room.Phase, repo.room.PlaybackState = RoomPhaseLobby, RoomPlaybackStateIdle
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	store := &fakeJoinRequests{repo: repo, requests: map[string]*JoinRequest{}}
	service.SetJoinRequestStore(store)
	hostConn := &recordingConn{}
	service.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", displayName: "Host", connection: hostConn}
	return service, repo, store, hostConn
}

func TestRoomVisibilityIsHostOnly(t *testing.T) {
	service, repo, _, hostConn := newPublicRoomsServiceForTest(t)
	ctx := t.Context()

	if _, err := service.SetRoomVisibility(ctx, repo.room.ID, 8, "guest", RoomVisibilityPublic); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("guest visibility error = %v", err)
	}
	if _, err := service.SetRoomVisibility(ctx, repo.room.ID, 7, "host", "everyone"); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("invalid visibility error = %v", err)
	}
	snapshot, err := service.SetRoomVisibility(ctx, repo.room.ID, 7, "host", RoomVisibilityPublic)
	if err != nil || snapshot.Visibility != RoomVisibilityPublic || repo.room.Visibility != RoomVisibilityPublic {
		t.Fatalf("host visibility = %+v, %v (stored %q)", snapshot.Visibility, err, repo.room.Visibility)
	}
	if len(hostConn.payloads) != 1 || hostConn.payloads[0]["type"] != snapshotMessageType {
		t.Fatalf("members not told the room was listed: %+v", hostConn.payloads)
	}
}

func TestJoinRequestsNeedAPublicRoomAndTheHost(t *testing.T) {
	service, repo, _, hostConn := newPublicRoomsServiceForTest(t)
	ctx := t.Context()
	roomID := repo.room.ID

	if _, err := service.RequestToJoin(ctx, roomID, 8, "guest"); !errors.Is(err, ErrRoomNotPublic) {
		t.Fatalf("private room request error = %v", err)
	}
	if _, err := service.SetRoomVisibility(ctx, roomID, 7, "host", RoomVisibilityPublic); err != nil {
		t.Fatal(err)
	}
	hostConn.payloads = nil
	if _, err := service.RequestToJoin(ctx, roomID, 7, "host"); !errors.Is(err, ErrInvalidJoinRequest) {
		t.Fatalf("host request error = %v", err)
	}

	request, err := service.RequestToJoin(ctx, roomID, 8, "guest")
	if err != nil || request.Status != JoinRequestStatusPending {
		t.Fatalf("request = %+v, %v", request, err)
	}
	if len(hostConn.payloads) != 1 || hostConn.payloads[0]["type"] != joinRequestsUpdateType {
		t.Fatalf("host not notified: %+v", hostConn.payloads)
	}
	again, err := service.RequestToJoin(ctx, roomID, 8, "guest")
	if err != nil || again.ID != request.ID || len(hostConn.payloads) != 1 {
		t.Fatalf("repeat request = %+v, %v, notifications %d", again, err, len(hostConn.payloads))
	}

	if _, err := service.ListJoinRequests(ctx, roomID, 8, "guest"); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("guest list error = %v", err)
	}
	if _, err := service.DecideJoinRequest(ctx, roomID, request.ID, 8, "guest", JoinRequestStatusApproved); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("guest self-approval error = %v", err)
	}
	// Another profile on the same account cannot read or ride on the request.
	if _, err := service.ReadOwnJoinRequest(ctx, roomID, request.ID, 8, "sibling"); !errors.Is(err, ErrJoinRequestNotFound) {
		t.Fatalf("other profile read error = %v", err)
	}
	if _, err := service.DecideJoinRequest(ctx, roomID, request.ID, 7, "host", JoinRequestStatusPending); !errors.Is(err, ErrInvalidJoinRequest) {
		t.Fatalf("pending decision error = %v", err)
	}

	pending, err := service.ListJoinRequests(ctx, roomID, 7, "host")
	if err != nil || len(pending) != 1 {
		t.Fatalf("host list = %+v, %v", pending, err)
	}
	if _, err := service.DecideJoinRequest(ctx, roomID, request.ID, 7, "host", JoinRequestStatusApproved); err != nil {
		t.Fatal(err)
	}
	read, err := service.ReadOwnJoinRequest(ctx, roomID, request.ID, 8, "guest")
	if err != nil || read.Status != JoinRequestStatusApproved {
		t.Fatalf("requester read = %+v, %v", read, err)
	}
	// Proof is already out, so an approval cannot quietly become a denial.
	if _, err := service.DecideJoinRequest(ctx, roomID, request.ID, 7, "host", JoinRequestStatusDenied); !errors.Is(err, ErrJoinRequestApproved) {
		t.Fatalf("deny after approve error = %v", err)
	}

	repo.room.Phase = RoomPhaseEnded
	if _, err := service.ReadOwnJoinRequest(ctx, roomID, request.ID, 8, "guest"); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("closed room read error = %v", err)
	}
}

func TestPublicRoomsCountOnlyLeasedMembers(t *testing.T) {
	service, repo, store, _ := newPublicRoomsServiceForTest(t)
	now := service.now()
	repo.room.Visibility = RoomVisibilityPublic
	runtime, err := json.Marshal(roomRuntime{Members: map[string]runtimeMember{
		buildMemberKey(7, "host"):  {UserID: 7, ProfileID: "host", DisplayName: "Brady", Connected: true, LeaseUntil: now.Add(time.Minute)},
		buildMemberKey(8, "guest"): {UserID: 8, ProfileID: "guest", Connected: true, LeaseUntil: now.Add(-time.Second)},
		buildMemberKey(9, "left"):  {UserID: 9, ProfileID: "left", Connected: false, LeaseUntil: now.Add(time.Minute)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store.runtime = runtime

	rooms, err := service.ListPublicRooms(t.Context(), 8, "guest")
	if err != nil || len(rooms) != 1 {
		t.Fatalf("rooms = %+v, %v", rooms, err)
	}
	room := rooms[0]
	if room.MemberCount != 1 || !room.HostConnected || room.HostDisplayName != "Brady" || room.IsHost || room.Phase != RoomPhaseLobby {
		t.Fatalf("listed room = %+v", room)
	}
}

// failingVisibilityRepo refuses every visibility write.
type failingVisibilityRepo struct{ *stubRepo }

func (failingVisibilityRepo) UpdateVisibility(context.Context, string, RoomVisibility, int64, int64) (*Room, error) {
	return nil, errors.New("database unavailable")
}

func TestFailedVisibilityWriteKeepsTheLiveRoom(t *testing.T) {
	now := time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	service := NewService(failingVisibilityRepo{repo}, &stubSessions{}, &stubFiles{}, nil, nil, nil)
	service.now = func() time.Time { return now }
	service.rooms[repo.room.ID] = &liveRoom{room: repo.room, members: make(map[string]*memberState)}

	if _, err := service.SetRoomVisibility(t.Context(), repo.room.ID, 7, "host", RoomVisibilityPublic); err == nil {
		t.Fatal("failed write reported success")
	}
	if got := service.rooms[repo.room.ID].room.visibility(); got != RoomVisibilityPrivate {
		t.Fatalf("live room visibility = %q after a failed write", got)
	}
}

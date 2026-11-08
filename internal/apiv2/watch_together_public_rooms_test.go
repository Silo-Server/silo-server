package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakePublicRooms struct {
	calls  int
	err    error
	status watchtogether.JoinRequestStatus
}

func (f *fakePublicRooms) request() watchtogether.JoinRequest {
	status := f.status
	if status == "" {
		status = watchtogether.JoinRequestStatusPending
	}
	return watchtogether.JoinRequest{ID: "req", RoomID: "room", UserID: 1, ProfileID: "p-owner", DisplayName: "Owner", Status: status, CreatedAt: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)}
}

func (f *fakePublicRooms) UpdateWatchTogetherVisibility(_ context.Context, room string, _ int, _ string, visibility watchtogether.RoomVisibility) (watchtogether.Snapshot, string, error) {
	f.calls++
	return watchtogether.Snapshot{RoomID: room, Phase: "lobby", PlaybackState: "idle", SelectionMode: "host_pick", GuestControlPolicy: "host_only", Visibility: visibility, AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host"}, "room-proof", f.err
}

func (f *fakePublicRooms) ListWatchTogetherPublicRooms(context.Context, int, string) ([]watchtogether.PublicRoom, error) {
	f.calls++
	content := "movie-1"
	request := f.request()
	return []watchtogether.PublicRoom{{RoomID: "room", HostUserID: 2, HostProfileID: "p-host", HostDisplayName: "Host", HostConnected: true, Phase: "playing", PlaybackState: "playing", SelectionMode: "host_pick", SelectedContentID: &content, MemberCount: 3, CreatedAt: request.CreatedAt, Request: &request}}, f.err
}

func (f *fakePublicRooms) RequestWatchTogetherJoin(context.Context, string, int, string) (watchtogether.JoinRequest, error) {
	f.calls++
	return f.request(), f.err
}

func (f *fakePublicRooms) ListWatchTogetherJoinRequests(context.Context, string, int, string) ([]watchtogether.JoinRequest, error) {
	f.calls++
	return []watchtogether.JoinRequest{f.request()}, f.err
}

func (f *fakePublicRooms) DecideWatchTogetherJoinRequest(_ context.Context, _, _ string, _ int, _ string, status watchtogether.JoinRequestStatus) (watchtogether.JoinRequest, error) {
	f.calls++
	f.status = status
	return f.request(), f.err
}

func (f *fakePublicRooms) ReadWatchTogetherJoinRequest(context.Context, string, string, int, string) (watchtogether.JoinRequest, *watchtogether.Snapshot, string, error) {
	f.calls++
	request := f.request()
	if request.Status != watchtogether.JoinRequestStatusApproved {
		return request, nil, "", f.err
	}
	return request, &watchtogether.Snapshot{RoomID: "room", Phase: "lobby", PlaybackState: "idle", SelectionMode: "host_pick", GuestControlPolicy: "host_only", Visibility: "public", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "guest"}, "room-proof", f.err
}

func TestWatchTogetherPublicRooms(t *testing.T) {
	f := new(fakePublicRooms)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherPublicRooms = f
	h := NewHandler(deps)
	room := Prefix + "/watch-together/rooms/room"

	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/watch-together/public-rooms", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPatch, room+"/visibility", `{"visibility":"friends"}`, profileOwner()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPatch, room+"/join-requests/req", `{"status":"pending"}`, profileOwner()), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid request dispatched")
	}

	rec := do(t, h, http.MethodPatch, room+"/visibility", `{"visibility":"public"}`, profileOwner())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"visibility":"public"`) {
		t.Fatalf("visibility: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, Prefix+"/watch-together/public-rooms", "", profileOwner())
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"phase":"playing"`) || !strings.Contains(body, `"join_request":{`) || strings.Contains(body, `"code"`) || strings.Contains(body, "room_access_token") {
		t.Fatalf("list: %d %s", rec.Code, body)
	}

	// A pending request carries no proof; an approved one does.
	rec = do(t, h, http.MethodGet, room+"/join-requests/req", "", profileOwner())
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "room_access_token") {
		t.Fatalf("pending read: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPatch, room+"/join-requests/req", `{"status":"approved"}`, profileOwner())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"approved"`) {
		t.Fatalf("decide: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, room+"/join-requests/req", "", profileOwner())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"room_access_token":"room-proof"`) {
		t.Fatalf("approved read: %d %s", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		err    error
		status int
	}{
		{watchtogether.ErrRoomForbidden, 403},
		{watchtogether.ErrRoomNotPublic, 409},
		{watchtogether.ErrJoinRequestApproved, 409},
		{watchtogether.ErrRoomClosed, 409},
		{watchtogether.ErrJoinRequestNotFound, 404},
		{watchtogether.ErrRoomNotFound, 404},
		{watchtogether.ErrInvalidJoinRequest, 422},
		{watchtogether.ErrPublicRoomsUnavailable, 503},
	} {
		f.err = tc.err
		rec = do(t, h, http.MethodPost, room+"/join-requests", "", profileOwner())
		if rec.Code != tc.status {
			t.Fatalf("%v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}

	deps.WatchTogetherPublicRooms = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, Prefix+"/watch-together/public-rooms", "", profileOwner()), TypeDependencyUnavailable)
}

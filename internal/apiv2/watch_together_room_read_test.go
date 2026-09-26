package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomRead struct {
	calls int
	err   error
}

func (f *fakeRoomRead) ReadWatchTogetherRoom(_ context.Context, room string, user int, profile, proof string) (watchtogether.Snapshot, string, error) {
	f.calls++
	if user != 1 || profile != "p-owner" || proof != "room-proof" {
		return watchtogether.Snapshot{}, "", &handlers.APIError{Status: 403, Message: "Room proof required."}
	}
	return watchtogether.Snapshot{RoomID: room, Phase: "playing", PlaybackState: "paused", SelectionMode: "host_pick", SelectionRevision: 2, SelectedFileID: new(7), SelectedLibraryID: new(8), GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host", Members: []watchtogether.MemberSummary{{UserID: 1, ProfileID: profile}}}, "renewed-proof", f.err
}
func TestWatchTogetherRoomRead(t *testing.T) {
	f := new(fakeRoomRead)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherRoomRead = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room-one"
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)
	if f.calls != 0 {
		t.Fatal("unauthenticated dispatch")
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", profileOwner()), TypePermissionDenied)
	headers := profileOwner()
	headers["X-Room-Token"] = "room-proof"
	rec := do(t, h, http.MethodGet, path, "", headers)
	for _, value := range []string{`"selected_file_id":"7"`, `"selected_library_id":"8"`, `"user_id":"1"`, `"room_access_token":"renewed-proof"`} {
		if !strings.Contains(rec.Body.String(), value) {
			t.Fatalf("missing %s: %s", value, rec.Body.String())
		}
	}
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	f.err = watchtogether.ErrRoomClosed
	requireProblem(t, do(t, h, http.MethodGet, path, "", headers), TypeConflict)
	f.err = watchtogether.ErrRoomNotFound
	requireProblem(t, do(t, h, http.MethodGet, path, "", headers), TypeNotFound)
	deps.WatchTogetherRoomRead = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, path, "", headers), TypeDependencyUnavailable)
}

package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomSelectionMode struct {
	calls, user   int
	room, profile string
	mode          watchtogether.RoomSelectionMode
	err           error
}

func (f *fakeRoomSelectionMode) UpdateWatchTogetherSelectionMode(_ context.Context, room string, user int, profile string, mode watchtogether.RoomSelectionMode) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.user = user
	f.room = room
	f.profile = profile
	f.mode = mode
	return watchtogether.Snapshot{RoomID: room, Phase: "lobby", PlaybackState: "idle", SelectionMode: mode, GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host"}, "room-proof", f.err
}

func TestWatchTogetherSelectionMode(t *testing.T) {
	f := new(fakeRoomSelectionMode)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherSelectionMode = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/selection-mode"
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"selection_mode":"vote"}`, nil), TypeAuthenticationRequired)
	for _, body := range []string{`{}`, `{"selection_mode":"dice"}`, `{"selection_mode":1}`} {
		requireProblem(t, do(t, h, http.MethodPatch, path, body, profileOwner()), TypeValidationFailed)
	}
	if f.calls != 0 {
		t.Fatal("invalid dispatch")
	}
	rec := do(t, h, http.MethodPatch, path, `{"selection_mode":"vote"}`, profileOwner())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"selection_mode":"vote"`) || f.mode != watchtogether.RoomSelectionModeVote || f.user != 1 || f.profile != "p-owner" || f.room != "room" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomForbidden, 403}, {watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrRoomClosed, 409}, {watchtogether.ErrRoomNotInLobby, 409}, {watchtogether.ErrInvalidSelection, 422}} {
		f.err = tc.err
		rec = do(t, h, http.MethodPatch, path, `{"selection_mode":"host_pick"}`, profileOwner())
		if rec.Code != tc.status {
			t.Fatalf("%v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
	deps.WatchTogetherSelectionMode = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPatch, path, `{"selection_mode":"vote"}`, profileOwner()), TypeDependencyUnavailable)
}

package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomStop struct {
	calls, user   int
	room, profile string
	err           error
}

func (f *fakeRoomStop) StopWatchTogetherPlayback(_ context.Context, room string, user int, profile string) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.user = user
	f.room = room
	f.profile = profile
	return watchtogether.Snapshot{RoomID: room, Phase: "lobby", PlaybackState: "idle", SelectionMode: "host_pick", SelectionRevision: 2, GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host", SelectedContentID: new("staged")}, "room-proof", f.err
}

func TestWatchTogetherStop(t *testing.T) {
	f := new(fakeRoomStop)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherStop = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/playback/stop"
	requireProblem(t, do(t, h, http.MethodPost, path, "", nil), TypeAuthenticationRequired)
	if f.calls != 0 {
		t.Fatal("unauthenticated dispatch")
	}
	rec := do(t, h, http.MethodPost, path, "", profileOwner())
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"phase":"lobby"`) || !strings.Contains(body, `"selected_content_id":"staged"`) || !strings.Contains(body, `"selection_revision":2`) || f.user != 1 || f.profile != "p-owner" || f.room != "room" {
		t.Fatalf("%d %s %+v", rec.Code, body, f)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomForbidden, 403}, {watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrRoomClosed, 409}} {
		f.err = tc.err
		rec = do(t, h, http.MethodPost, path, "", profileOwner())
		if rec.Code != tc.status {
			t.Fatalf("%v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
	deps.WatchTogetherStop = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, "", profileOwner()), TypeDependencyUnavailable)
}

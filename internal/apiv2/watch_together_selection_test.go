package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomSelection struct {
	calls, user   int
	room, profile string
	input         watchtogether.SelectItemInput
	err           error
}

func (f *fakeRoomSelection) SelectWatchTogetherItem(_ context.Context, room string, user int, profile string, input watchtogether.SelectItemInput) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.user = user
	f.room = room
	f.profile = profile
	f.input = input
	return watchtogether.Snapshot{RoomID: room, Phase: "playing", PlaybackState: "waiting", SelectionMode: "host_pick", GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host", SelectedContentID: new("winner")}, "room-proof", f.err
}
func TestWatchTogetherSelection(t *testing.T) {
	f := new(fakeRoomSelection)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherSelection = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/selection"
	requireProblem(t, do(t, h, http.MethodPut, path, `{"content_id":"movie"}`, nil), TypeAuthenticationRequired)
	for _, body := range []string{`{}`, `{"content_id":"movie","file_id":"0"}`, `{"content_id":"movie","library_id":"bad"}`, `{"content_id":"movie","file_id":7}`} {
		requireProblem(t, do(t, h, http.MethodPut, path, body, profileOwner()), TypeValidationFailed)
	}
	if f.calls != 0 {
		t.Fatal("invalid dispatch")
	}
	rec := do(t, h, http.MethodPut, path, `{"content_id":"movie","file_id":"7","library_id":"8"}`, profileOwner())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"selected_content_id":"winner"`) || f.input.ContentID != "movie" || *f.input.FileID != 7 || *f.input.LibraryID != 8 || f.user != 1 || f.profile != "p-owner" || f.room != "room" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomForbidden, 403}, {watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrRoomClosed, 409}, {watchtogether.ErrVoteRoomSelection, 409}, {watchtogether.ErrInvalidSelection, 422}} {
		f.err = tc.err
		rec = do(t, h, http.MethodPut, path, `{"content_id":"movie"}`, profileOwner())
		if rec.Code != tc.status {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	deps.WatchTogetherSelection = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPut, path, `{"content_id":"movie"}`, profileOwner()), TypeDependencyUnavailable)
}

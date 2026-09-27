package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomClose struct {
	room, profile string
	user, calls   int
	err           error
}

func (f *fakeRoomClose) CloseWatchTogetherRoom(_ context.Context, room string, user int, profile string) error {
	f.room = room
	f.user = user
	f.profile = profile
	f.calls++
	return f.err
}
func TestWatchTogetherClose(t *testing.T) {
	f := new(fakeRoomClose)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherClose = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room-one"
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	if f.calls != 0 {
		t.Fatal("unauthenticated dispatch")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{nil, 204}, {watchtogether.ErrRoomForbidden, 403}, {watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrRoomClosed, 409}} {
		f.err = tc.err
		rec := do(t, h, http.MethodDelete, path, "", profileOwner())
		if rec.Code != tc.status {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if tc.status == 204 && rec.Body.Len() != 0 {
			t.Fatal("receipt body")
		}
	}
	if f.room != "room-one" || f.user != 1 || f.profile != "p-owner" {
		t.Fatalf("scope: %+v", f)
	}
	deps.WatchTogetherClose = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodDelete, path, "", profileOwner()), TypeDependencyUnavailable)
}

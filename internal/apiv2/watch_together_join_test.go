package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomJoin struct {
	calls, user int
	profile     string
	input       watchtogether.JoinInput
	err         error
}

func (f *fakeRoomJoin) JoinWatchTogetherRoom(_ context.Context, user int, profile string, in watchtogether.JoinInput) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.user = user
	f.profile = profile
	f.input = in
	return watchtogether.Snapshot{RoomID: "joined", Phase: "lobby", PlaybackState: "idle", SelectionMode: "host_pick", GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "guest"}, "proof", f.err
}
func TestWatchTogetherJoin(t *testing.T) {
	f := new(fakeRoomJoin)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherJoin = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/join"
	requireProblem(t, do(t, h, http.MethodPost, path, `{"code":"ROOM"}`, nil), TypeAuthenticationRequired)
	for _, body := range []string{`{}`, `{"code":"  ","join_token":" "}`, `{"code":1}`} {
		requireProblem(t, do(t, h, http.MethodPost, path, body, profileOwner()), TypeValidationFailed)
	}
	if f.calls != 0 {
		t.Fatal("invalid dispatch")
	}
	r := do(t, h, http.MethodPost, path, `{"code":" ROOM ","join_token":" invite "}`, profileOwner())
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"room_id":"joined"`) || f.user != 1 || f.profile != "p-owner" || f.input.Code != "ROOM" || f.input.JoinToken != "invite" {
		t.Fatalf("%d %s %+v", r.Code, r.Body.String(), f)
	}
	if !strings.Contains(r.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("room proof cacheable")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrRoomClosed, 409}, {watchtogether.ErrInvalidJoinRequest, 422}} {
		f.err = tc.err
		r = do(t, h, http.MethodPost, path, `{"code":"ROOM"}`, profileOwner())
		if r.Code != tc.status {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
	}
	deps.WatchTogetherJoin = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, `{"code":"ROOM"}`, profileOwner()), TypeDependencyUnavailable)
}

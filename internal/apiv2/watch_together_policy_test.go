package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomPolicy struct {
	calls, user   int
	room, profile string
	policy        watchtogether.GuestControlPolicy
	err           error
}

func (f *fakeRoomPolicy) UpdateWatchTogetherPolicy(_ context.Context, room string, user int, profile string, policy watchtogether.GuestControlPolicy) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.user = user
	f.room = room
	f.profile = profile
	f.policy = policy
	return watchtogether.Snapshot{RoomID: room, Phase: "playing", PlaybackState: "paused", SelectionMode: "host_pick", GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host"}, "room-proof", f.err
}
func TestWatchTogetherPolicy(t *testing.T) {
	f := new(fakeRoomPolicy)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherPolicy = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/policy"
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"guest_control_policy":"guest_play_pause"}`, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"guest_control_policy":"invalid"}`, profileOwner()), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid dispatch")
	}
	rec := do(t, h, http.MethodPatch, path, `{"guest_control_policy":"guest_play_pause"}`, profileOwner())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"guest_control_policy":"host_only"`) || f.policy != "guest_play_pause" || f.user != 1 || f.profile != "p-owner" || f.room != "room" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomForbidden, 403}, {watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrRoomClosed, 409}} {
		f.err = tc.err
		rec = do(t, h, http.MethodPatch, path, `{"guest_control_policy":"host_only"}`, profileOwner())
		if rec.Code != tc.status {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	deps.WatchTogetherPolicy = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPatch, path, `{"guest_control_policy":"host_only"}`, profileOwner()), TypeDependencyUnavailable)
}

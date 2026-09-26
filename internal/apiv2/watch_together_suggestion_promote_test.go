package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakePromotion struct {
	calls, user       int
	room, id, profile string
	err               error
}

func (f *fakePromotion) CheckSuggestionRoomProof(room string, user int, profile, proof string) error {
	if proof != "proof" {
		return &handlers.APIError{Status: 403, Message: "proof required"}
	}
	return nil
}
func (f *fakePromotion) PromoteRoomSuggestion(_ context.Context, room, id string, user int, profile string) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.room = room
	f.id = id
	f.user = user
	f.profile = profile
	return watchtogether.Snapshot{RoomID: room, Phase: "playing", PlaybackState: "waiting", SelectionMode: "vote", GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host"}, "renewed", f.err
}
func TestSuggestionPromotionV2(t *testing.T) {
	f := new(fakePromotion)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherSuggestionPromote = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/suggestions/promote"
	body := `{"suggestion_id":"winner"}`
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, body, profileOwner()), TypePermissionDenied)
	headers := profileOwner()
	headers["X-Room-Token"] = "proof"
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, headers), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid dispatch")
	}
	r := do(t, h, http.MethodPost, path, body, headers)
	if r.Code != 200 || f.room != "room" || f.id != "winner" || f.user != 1 || f.profile != "p-owner" {
		t.Fatalf("%d %s %+v", r.Code, r.Body.String(), f)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomForbidden, 403}, {watchtogether.ErrSuggestionNotFound, 404}, {watchtogether.ErrRoomClosed, 409}, {watchtogether.ErrVoteRoomSelection, 409}, {watchtogether.ErrInvalidSelection, 422}, {watchtogether.ErrSuggestionPromotionUnavailable, 503}} {
		f.err = tc.err
		r = do(t, h, http.MethodPost, path, body, headers)
		if r.Code != tc.status {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
	}
	deps.WatchTogetherSuggestionPromote = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, body, headers), TypeDependencyUnavailable)
}

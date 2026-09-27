package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomCreate struct {
	calls, user int
	id, profile string
	mode        watchtogether.RoomSelectionMode
	err         error
}

func (f *fakeRoomCreate) CreateWatchTogetherRoom(_ context.Context, id string, user int, profile string, mode watchtogether.RoomSelectionMode) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.id = id
	f.user = user
	f.profile = profile
	f.mode = mode
	return watchtogether.Snapshot{RoomID: id, Phase: "lobby", PlaybackState: "idle", SelectionMode: mode, GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host"}, "proof", f.err
}
func TestWatchTogetherCreate(t *testing.T) {
	f := new(fakeRoomCreate)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherCreate = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms"
	body := `{"room_id":"00000000-0000-4000-8000-000000000001","selection_mode":"vote"}`
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
	for _, invalid := range []string{`{}`, `{"room_id":"bad","selection_mode":"vote"}`, `{"room_id":"00000000-0000-4000-8000-000000000001"}`, `{"room_id":"00000000-0000-4000-8000-000000000001","selection_mode":"bad"}`} {
		requireProblem(t, do(t, h, http.MethodPost, path, invalid, profileOwner()), TypeValidationFailed)
	}
	if f.calls != 0 {
		t.Fatal("invalid dispatch")
	}
	r := do(t, h, http.MethodPost, path, body, profileOwner())
	if r.Code != 201 || f.user != 1 || f.profile != "p-owner" || f.mode != "vote" || !strings.Contains(r.Body.String(), f.id) || !strings.Contains(r.Body.String(), `"room_access_token":"proof"`) {
		t.Fatalf("%d %s %+v", r.Code, r.Body.String(), f)
	}
	if !strings.Contains(r.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("cacheable proof")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomCreationIdentityConflict, 409}, {watchtogether.ErrRoomClosed, 409}, {watchtogether.ErrInvalidSelection, 422}, {watchtogether.ErrRoomCreateUnavailable, 503}} {
		f.err = tc.err
		r = do(t, h, http.MethodPost, path, body, profileOwner())
		if r.Code != tc.status {
			t.Fatalf("%v: %d %s", tc.err, r.Code, r.Body.String())
		}
	}
	deps.WatchTogetherCreate = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, body, profileOwner()), TypeDependencyUnavailable)
}

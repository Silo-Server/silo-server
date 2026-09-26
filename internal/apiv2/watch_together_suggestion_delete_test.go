package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeSuggestionDelete struct {
	fakeRoomSuggestions
	calls             int
	err               error
	room, id, profile string
	user              int
}

func (f *fakeSuggestionDelete) DeleteRoomSuggestion(_ context.Context, room, id string, user int, profile string) error {
	f.calls++
	f.room, f.id, f.user, f.profile = room, id, user, profile
	return f.err
}
func TestWatchTogetherSuggestionDelete(t *testing.T) {
	f := new(fakeSuggestionDelete)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherSuggestionDelete = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/suggestions/target"
	for _, headers := range []map[string]string{nil, bearer(memberToken), profileOwner()} {
		rec := do(t, h, http.MethodDelete, path, "", headers)
		if rec.Code < 400 || f.calls != 0 {
			t.Fatalf("unproved=%d calls=%d", rec.Code, f.calls)
		}
	}
	headers := profileOwner()
	headers["X-Room-Token"] = "room-proof"
	for _, tc := range []struct {
		err    error
		status int
	}{{nil, 204}, {watchtogether.ErrSuggestionNotFound, 404}, {watchtogether.ErrRoomForbidden, 403}, {watchtogether.ErrRoomClosed, 409}} {
		f.err = tc.err
		before := f.calls
		rec := do(t, h, http.MethodDelete, path, "", headers)
		if rec.Code != tc.status || f.calls != before+1 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, f.calls, rec.Body.String())
		}
		if tc.status == 204 && rec.Body.Len() != 0 {
			t.Fatal("receipt has body")
		}
	}
	if f.room != "room" || f.id != "target" || f.user != 1 || f.profile != "p-owner" {
		t.Fatalf("scope=%+v", f)
	}
}

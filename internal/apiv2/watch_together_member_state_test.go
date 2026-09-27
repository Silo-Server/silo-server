package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeMemberState struct {
	calls, user   int
	room, profile string
	ids           []string
	filter        catalogpkg.AccessFilter
	proofErr, err error
}

func (f *fakeMemberState) CheckSuggestionRoomProof(room string, user int, profile, token string) error {
	if f.proofErr != nil {
		return f.proofErr
	}
	if token != "room-proof" {
		return &handlers.APIError{Status: 403, Code: "forbidden", Message: "Room access token required"}
	}
	return nil
}

func (f *fakeMemberState) RoomMemberState(_ context.Context, room string, user int, profile string, ids []string, filter catalogpkg.AccessFilter) ([]watchtogether.MemberSummary, []watchtogether.ItemMemberState, error) {
	f.calls++
	f.room, f.user, f.profile, f.ids = room, user, profile, ids
	f.filter = filter
	if f.err != nil {
		return nil, nil, f.err
	}
	position := 120.0
	return []watchtogether.MemberSummary{{UserID: 1, ProfileID: "p-owner", DisplayName: "Owner", IsHost: true, IsSelf: true, Connected: true}, {UserID: 2, ProfileID: "p-guest", DisplayName: "Guest", Connected: true, LobbyReady: true}},
		[]watchtogether.ItemMemberState{{ContentID: "movie", Members: []watchtogether.MemberWatchState{
			{UserID: 1, ProfileID: "p-owner", State: watchtogether.MemberWatchStateInProgress, PositionSeconds: &position, OnWatchlist: true},
			{UserID: 2, ProfileID: "p-guest", State: watchtogether.MemberWatchStateUnseen},
		}}}, nil
}

func TestWatchTogetherMemberState(t *testing.T) {
	f := new(fakeMemberState)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherMemberState = f
	deps.CatalogAccess = &fakeCatalog{}
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/member-state"
	body := `{"content_ids":["movie","series"]}`
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, body, profileOwner()), TypePermissionDenied)
	proof := with(profileOwner(), "X-Room-Token", "room-proof")
	for _, invalid := range []string{`{}`, `{"content_ids":[]}`, `{"content_ids":[""]}`, `{"content_ids":[` + strings.Repeat(`"x",`, 200) + `"y"]}`} {
		requireProblem(t, do(t, h, http.MethodPost, path, invalid, proof), TypeValidationFailed)
	}
	if f.calls != 0 {
		t.Fatal("invalid dispatch")
	}
	rec := do(t, h, http.MethodPost, path, body, proof)
	out := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || f.room != "room" || f.user != 1 || f.profile != "p-owner" || len(f.ids) != 2 || f.filter.UserID != 1 || f.filter.ProfileID != "p-owner" || len(f.filter.AllowedLibraryIDs) != 2 {
		t.Fatalf("%d %s %+v", rec.Code, out, f)
	}
	for _, want := range []string{`"content_id":"movie"`, `"state":"in_progress"`, `"position_seconds":120`, `"on_watchlist":true`, `"state":"unseen"`, `"lobby_ready":true`, `"user_id":"2"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrRoomClosed, 409}} {
		f.err = tc.err
		if rec := do(t, h, http.MethodPost, path, body, proof); rec.Code != tc.status {
			t.Fatalf("%v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
	f.err = nil
	f.proofErr = &handlers.APIError{Status: 503, Code: "unavailable", Message: "Watch together is unavailable"}
	if rec := do(t, h, http.MethodPost, path, body, proof); rec.Code != 503 {
		t.Fatalf("unavailable proof: %d", rec.Code)
	}
	deps.WatchTogetherMemberState = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, body, proof), TypeDependencyUnavailable)
	deps.WatchTogetherMemberState = f
	f.proofErr = nil
	deps.CatalogAccess = nil
	before := f.calls
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, body, proof), TypeDependencyUnavailable)
	if f.calls != before {
		t.Fatal("member state dispatched without catalog access")
	}
}

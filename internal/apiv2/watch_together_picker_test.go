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

type fakePicker struct {
	calls, user   int
	room, profile string
	filter        catalogpkg.AccessFilter
	err           error
}

func (f *fakePicker) CheckSuggestionRoomProof(room string, user int, profile, token string) error {
	if token != "room-proof" {
		return &handlers.APIError{Status: 403, Code: "forbidden", Message: "Room access token required"}
	}
	return nil
}

func (f *fakePicker) RoomPicker(_ context.Context, room string, user int, profile string, filter catalogpkg.AccessFilter) (watchtogether.PickerView, error) {
	f.calls++
	f.room, f.user, f.profile, f.filter = room, user, profile, filter
	if f.err != nil {
		return watchtogether.PickerView{}, f.err
	}
	position := 600.0
	return watchtogether.PickerView{
		Members: []watchtogether.MemberSummary{{UserID: 1, ProfileID: "p-owner", DisplayName: "Owner", IsHost: true, IsSelf: true, Connected: true}},
		ContinueTogether: []watchtogether.PickerViewEntry{{
			Item:    &catalogpkg.ItemDetail{ContentID: "severance", Type: "series", Title: "Severance", Genres: []string{"Drama"}},
			Members: []watchtogether.PickerMember{{UserID: 1, ProfileID: "p-owner", DisplayName: "Owner", PositionSeconds: &position}},
			NextUp:  &watchtogether.PickerNextUp{ContentID: "sev-s2e4", SeasonNumber: 2, EpisodeNumber: 4, Title: "Woe's Hollow", MemberCount: 2},
		}, {Item: nil}},
		WatchlistUnion: []watchtogether.PickerViewEntry{{Item: &catalogpkg.ItemDetail{ContentID: "arrival", Type: "movie", Title: "Arrival"}, Members: []watchtogether.PickerMember{{UserID: 1, ProfileID: "p-owner", DisplayName: "Owner"}}}},
	}, nil
}

func TestWatchTogetherPicker(t *testing.T) {
	f := new(fakePicker)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherPicker = f
	deps.CatalogAccess = &fakeCatalog{}
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/picker"
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, path, "", profileOwner()), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("dispatched without proof")
	}
	proof := with(profileOwner(), "X-Room-Token", "room-proof")
	rec := do(t, h, http.MethodGet, path, "", proof)
	out := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || f.room != "room" || f.user != 1 || f.profile != "p-owner" || f.filter.UserID != 1 {
		t.Fatalf("%d %s %+v", rec.Code, out, f)
	}
	for _, want := range []string{`"content_id":"severance"`, `"title":"Severance"`, `"display_name":"Owner"`, `"position_seconds":600`, `"next_up":{"content_id":"sev-s2e4"`, `"member_count":2`, `"content_id":"arrival"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	if strings.Count(out, `"item":{`) != 2 {
		t.Fatalf("an entry without a visible item leaked: %s", out)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{watchtogether.ErrRoomNotFound, 404}, {watchtogether.ErrRoomClosed, 409}} {
		f.err = tc.err
		if rec := do(t, h, http.MethodGet, path, "", proof); rec.Code != tc.status {
			t.Fatalf("%v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
	deps.WatchTogetherPicker = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, path, "", proof), TypeDependencyUnavailable)
}

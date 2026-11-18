package apiv2

import (
	"context"

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

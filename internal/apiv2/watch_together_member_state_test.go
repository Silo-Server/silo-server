package apiv2

import (
	"context"

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

package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomStage struct {
	calls, user   int
	room, profile string
	input         watchtogether.SelectItemInput
	err           error
}

func (f *fakeRoomStage) StageWatchTogetherItem(_ context.Context, room string, user int, profile string, input watchtogether.SelectItemInput) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.user = user
	f.room = room
	f.profile = profile
	f.input = input
	return watchtogether.Snapshot{RoomID: room, Phase: "lobby", PlaybackState: "idle", SelectionMode: "host_pick", GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host", SelectedContentID: new("staged"), Members: []watchtogether.MemberSummary{{UserID: 1, ProfileID: "p-owner", DisplayName: "Owner", IsHost: true, IsSelf: true, Connected: true, LobbyReady: true}}}, "room-proof", f.err
}

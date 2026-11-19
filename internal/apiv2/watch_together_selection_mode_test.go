package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomSelectionMode struct {
	calls, user   int
	room, profile string
	mode          watchtogether.RoomSelectionMode
	err           error
}

func (f *fakeRoomSelectionMode) UpdateWatchTogetherSelectionMode(_ context.Context, room string, user int, profile string, mode watchtogether.RoomSelectionMode) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.user = user
	f.room = room
	f.profile = profile
	f.mode = mode
	return watchtogether.Snapshot{RoomID: room, Phase: "lobby", PlaybackState: "idle", SelectionMode: mode, GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host"}, "room-proof", f.err
}

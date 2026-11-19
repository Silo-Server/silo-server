package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomStart struct {
	calls, user   int
	room, profile string
	err           error
}

func (f *fakeRoomStart) StartWatchTogetherPlayback(_ context.Context, room string, user int, profile string) (watchtogether.Snapshot, string, error) {
	f.calls++
	f.user = user
	f.room = room
	f.profile = profile
	return watchtogether.Snapshot{RoomID: room, Phase: "playing", PlaybackState: "waiting", SelectionMode: "host_pick", SelectionRevision: 1, GuestControlPolicy: "host_only", AnchorUpdatedAt: "2026-01-01T00:00:00Z", SelfRole: "host", SelectedContentID: new("staged")}, "room-proof", f.err
}

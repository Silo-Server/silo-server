package apiv2

import (
	"context"

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

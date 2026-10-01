package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) FallbackWatchTogetherSource(ctx context.Context, room string, user int, profile, proof string, input watchtogether.SourceFallbackInput) (watchtogether.Snapshot, string, error) {
	if err := h.CheckSuggestionRoomProof(room, user, profile, proof); err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	snapshot, err := h.Service.FallbackSource(ctx, room, user, profile, input)
	if err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	response, err := h.buildRoomResponse(ctx, snapshot, user, profile)
	return response.Room, response.RoomAccessToken, err
}

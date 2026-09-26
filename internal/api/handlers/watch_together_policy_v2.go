package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) UpdateWatchTogetherPolicy(ctx context.Context, room string, user int, profile string, policy watchtogether.GuestControlPolicy) (watchtogether.Snapshot, string, error) {
	if h == nil || h.Service == nil || h.TokenService == nil {
		return watchtogether.Snapshot{}, "", apiError(503, "unavailable", "Watch together is unavailable")
	}
	snapshot, err := h.Service.UpdatePolicy(ctx, room, user, profile, policy)
	if err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	response, err := h.buildRoomResponse(ctx, snapshot, user, profile)
	return response.Room, response.RoomAccessToken, err
}

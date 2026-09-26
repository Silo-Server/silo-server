package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) SelectWatchTogetherItem(ctx context.Context, room string, user int, profile string, input watchtogether.SelectItemInput) (watchtogether.Snapshot, string, error) {
	if h == nil || h.Service == nil || h.TokenService == nil {
		return watchtogether.Snapshot{}, "", apiError(503, "unavailable", "Watch together is unavailable")
	}
	snapshot, err := h.Service.SelectItemOnce(ctx, room, user, profile, input)
	if err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	response, err := h.buildRoomResponse(ctx, snapshot, user, profile)
	return response.Room, response.RoomAccessToken, err
}

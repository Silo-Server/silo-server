package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) JoinWatchTogetherRoom(ctx context.Context, user int, profile string, input watchtogether.JoinInput) (watchtogether.Snapshot, string, error) {
	if h == nil || h.Service == nil || h.TokenService == nil {
		return watchtogether.Snapshot{}, "", apiError(503, "unavailable", "Watch together is unavailable")
	}
	room, err := h.Service.JoinRoom(ctx, input)
	if err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	snapshot, err := h.Service.Snapshot(ctx, room.ID, user, profile)
	if err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	response, err := h.buildRoomResponse(ctx, snapshot, user, profile)
	return response.Room, response.RoomAccessToken, err
}

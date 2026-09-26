package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) CreateWatchTogetherRoom(ctx context.Context, id string, user int, profile string, mode watchtogether.RoomSelectionMode) (watchtogether.Snapshot, string, error) {
	if h == nil || h.Service == nil || h.TokenService == nil {
		return watchtogether.Snapshot{}, "", apiError(503, "unavailable", "Watch together is unavailable")
	}
	snapshot, err := h.Service.CreateRoomWithIdentity(ctx, id, watchtogether.CreateRoomInput{HostUserID: user, HostProfileID: profile, SelectionMode: mode})
	if err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	response, err := h.buildRoomResponse(ctx, snapshot, user, profile)
	return response.Room, response.RoomAccessToken, err
}

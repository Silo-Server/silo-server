package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) PromoteRoomSuggestion(ctx context.Context, room, id string, user int, profile string) (watchtogether.Snapshot, string, error) {
	if h == nil || h.Service == nil || h.TokenService == nil {
		return watchtogether.Snapshot{}, "", watchtogether.ErrSuggestionPromotionUnavailable
	}
	snapshot, err := h.Service.PromoteSuggestionOnce(ctx, room, id, user, profile)
	if err != nil {
		return watchtogether.Snapshot{}, "", err
	}
	response, err := h.buildRoomResponse(ctx, snapshot, user, profile)
	return response.Room, response.RoomAccessToken, err
}

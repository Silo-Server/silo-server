package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) CreateRoomSuggestion(ctx context.Context, room, id string, user int, profile string, input watchtogether.CreateSuggestionInput) (string, error) {
	if h == nil || h.Service == nil || h.TokenService == nil {
		return "", watchtogether.ErrSuggestionCreateUnavailable
	}
	return h.Service.CreateSuggestionWithIdentity(ctx, room, id, user, profile, input)
}

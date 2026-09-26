package handlers

import "context"

// CloseWatchTogetherRoom keeps host authorization and room-close dispatch in the domain.
func (h *WatchTogetherHandler) CloseWatchTogetherRoom(ctx context.Context, room string, user int, profile string) error {
	if h == nil || h.Service == nil {
		return apiError(503, "unavailable", "Watch together is not available")
	}
	return h.Service.CloseRoom(ctx, room, user, profile)
}

package handlers

import "context"

// DeleteRoomSuggestion retains the owning service's host/suggester check and
// broadcast path. Missing IDs remain not-found, including repeated deletion.
func (h *WatchTogetherHandler) DeleteRoomSuggestion(ctx context.Context, room, suggestion string, user int, profile string) error {
	_, err := h.Service.DeleteSuggestion(ctx, room, suggestion, user, profile)
	return err
}

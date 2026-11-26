package handlers

import "context"

// DeleteRoomSuggestion retains the owning service's host/suggester check and
// broadcast path. Missing IDs remain not-found, including repeated deletion.
// ownOnly drops the host's power to delete any suggestion, for a session
// another app opened.
func (h *WatchTogetherHandler) DeleteRoomSuggestion(ctx context.Context, room, suggestion string, user int, profile string, ownOnly bool) error {
	if ownOnly {
		_, err := h.Service.DeleteOwnSuggestion(ctx, room, suggestion, user, profile)
		return err
	}
	_, err := h.Service.DeleteSuggestion(ctx, room, suggestion, user, profile)
	return err
}

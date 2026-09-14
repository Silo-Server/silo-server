package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func (h *WatchTogetherHandler) CheckSuggestionRoomProof(room string, user int, profile, token string) error {
	if h == nil || h.Service == nil || h.TokenService == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Watch together is unavailable")
	}
	claims, err := h.TokenService.Validate(token)
	if err != nil || claims.RoomID != room || claims.UserID != user || claims.ProfileID != profile {
		return apiError(http.StatusForbidden, "forbidden", "Room access token required")
	}
	return nil
}
func (h *WatchTogetherHandler) ListSuggestionPage(ctx context.Context, room string, user int, profile string, limit int, after *watchtogether.SuggestionPosition) ([]watchtogether.Suggestion, bool, error) {
	return h.Service.ListSuggestionsPage(ctx, room, user, profile, limit, after)
}
func (h *WatchTogetherHandler) SetSuggestionVote(ctx context.Context, room, suggestion string, user int, profile string, vote bool) error {
	var err error
	if vote {
		_, err = h.Service.Vote(ctx, room, suggestion, user, profile)
	} else {
		_, err = h.Service.Unvote(ctx, room, suggestion, user, profile)
	}
	// Both repository no-op outcomes occur before tally changes or broadcasts.
	if (vote && errors.Is(err, watchtogether.ErrDuplicateVote)) || (!vote && errors.Is(err, watchtogether.ErrNotVoted)) {
		return nil
	}
	return err
}

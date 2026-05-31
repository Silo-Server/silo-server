package handlers

import (
	"context"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type userStateEventPayload struct {
	ProfileID   string `json:"profile_id"`
	ContentID   string `json:"content_id,omitempty"`
	SeriesID    string `json:"series_id,omitempty"`
	Change      string `json:"change"`
	Played      *bool  `json:"played,omitempty"`
	IsFavorite  *bool  `json:"is_favorite,omitempty"`
	InWatchlist *bool  `json:"in_watchlist,omitempty"`
}

type userStateEventState struct {
	Played      *bool
	IsFavorite  *bool
	InWatchlist *bool
}

const userStateChangedEvent = "user_state.changed"

func publishUserStateEvent(
	ctx context.Context,
	hub *evt.Hub,
	userID int,
	profileID, contentID, seriesID, change string,
	state userStateEventState,
) {
	if hub == nil || userID == 0 || profileID == "" || change == "" {
		return
	}
	_ = hub.PublishJSON(ctx, evt.ChannelUserState, userStateChangedEvent, userStateEventPayload{
		ProfileID:   profileID,
		ContentID:   contentID,
		SeriesID:    seriesID,
		Change:      change,
		Played:      state.Played,
		IsFavorite:  state.IsFavorite,
		InWatchlist: state.InWatchlist,
	}, evt.PublishOptions{
		UserID:    userID,
		ProfileID: profileID,
	})
}

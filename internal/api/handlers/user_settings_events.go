package handlers

import (
	"context"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// userSettingsEventPayload deliberately carries the identity of what changed
// and never the value. Admins receive every user's user-scoped events (see
// allowsEventForClaims in events_ws.go), so a value here would leak private
// settings to admins. Clients that care about the new value re-fetch it over
// the REST API, where access is scoped to the caller's own session.
type userSettingsEventPayload struct {
	Key       string `json:"key"`
	Scope     string `json:"scope"`
	ProfileID string `json:"profile_id,omitempty"`
}

const userSettingsChangedEvent = "user_settings.changed"

func publishUserSettingsEvent(
	ctx context.Context,
	hub *evt.Hub,
	userID int,
	profileID, key, scope string,
) {
	if hub == nil || userID == 0 || key == "" || scope == "" {
		return
	}
	// The payload is always non-empty: an empty Data would fall back to a null
	// snapshot frame in the hub, telling subscribers nothing at all.
	_ = hub.PublishJSON(ctx, evt.ChannelUserSettings, userSettingsChangedEvent, userSettingsEventPayload{
		Key:       key,
		Scope:     scope,
		ProfileID: profileID,
	}, evt.PublishOptions{
		UserID:    userID,
		ProfileID: profileID,
	})
}

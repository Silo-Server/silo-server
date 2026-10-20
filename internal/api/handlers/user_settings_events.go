package handlers

import (
	"context"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

// userSettingsEventPayload deliberately carries the identity of what changed
// and never the value. Recipients re-fetch values over the REST API under
// their current session and profile authority; the socket only invalidates
// their settings cache.
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

// publishAdminDefaultEvent tells every connected client that the
// administrator's default for a setting changed, so the viewers inheriting it
// resolve again. It is addressed to no account because every viewer without a
// stored choice is affected; like every settings event it names the key and
// never the value. Server-settings keys that hold no administrator default are
// ignored.
func publishAdminDefaultEvent(ctx context.Context, hub *evt.Hub, serverSettingKey string) {
	if hub == nil {
		return
	}
	contract, err := settingscontract.Load()
	if err != nil {
		return
	}
	def, ok := contract.AdminDefaultFor(serverSettingKey)
	if !ok {
		return
	}
	_ = hub.PublishJSON(ctx, evt.ChannelUserSettings, userSettingsChangedEvent, userSettingsEventPayload{
		Key:   def.Key,
		Scope: string(settingscontract.ScopeDefault),
	}, evt.PublishOptions{})
}

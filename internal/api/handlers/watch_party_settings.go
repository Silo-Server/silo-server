package handlers

import (
	"context"
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// WatchPartyGuestControlPreferences reads and writes the host profile's saved
// Watch Party guest-control policy through the canonical settings store, so a
// save publishes the same user_settings.changed event as a settings-screen
// write.
func (h *SettingValuesHandler) WatchPartyGuestControlPreferences() watchtogether.GuestControlPreferences {
	return watchPartyGuestControlPreferences{settings: h}
}

type watchPartyGuestControlPreferences struct {
	settings *SettingValuesHandler
}

func (p watchPartyGuestControlPreferences) GuestControlPolicy(
	ctx context.Context, userID int, profileID string,
) (watchtogether.GuestControlPolicy, error) {
	store, err := p.settings.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return "", err
	}
	resolved, err := p.settings.resolver.Resolve(ctx, store, settingsresolve.Context{ProfileID: profileID},
		[]string{settingskeys.WatchPartyGuestControlPolicy}, nil)
	if err != nil {
		return "", err
	}
	for _, value := range resolved {
		if value.Key != settingskeys.WatchPartyGuestControlPolicy {
			continue
		}
		var policy watchtogether.GuestControlPolicy
		if err := json.Unmarshal(value.Value, &policy); err != nil {
			return "", err
		}
		return policy, nil
	}
	return watchtogether.GuestControlPolicyHostOnly, nil
}

func (p watchPartyGuestControlPreferences) SaveGuestControlPolicy(
	ctx context.Context, userID int, profileID string, policy watchtogether.GuestControlPolicy,
) error {
	store, err := p.settings.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return err
	}
	value, _ := json.Marshal(policy)
	identity := userstore.SettingIdentity{
		Key:       settingskeys.WatchPartyGuestControlPolicy,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: profileID,
	}
	if _, apiErr := p.settings.writeSettingValue(ctx, store, userID, identity, value, "", DeviceMetadata{}); apiErr != nil {
		return apiErr
	}
	return nil
}

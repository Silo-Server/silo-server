package handlers

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (h *SettingValuesHandler) ListAdminAccountSettings(ctx context.Context, userID int, after userstore.SettingIdentity, limit int) ([]SettingValueView, bool, error) {
	store, err := h.storeOf(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	pager, ok := store.(userstore.AdminSettingValuePager)
	if !ok {
		return nil, false, apiError(501, "capability_unsupported", "Administrator settings pagination is unavailable")
	}
	rows, more, pageErr := pager.ListAdminSettingValuesPage(ctx, after, limit)
	if pageErr != nil {
		return nil, false, pageErr
	}
	out := make([]SettingValueView, 0, len(rows))
	for _, row := range rows {
		out = append(out, settingValueToResponse(row))
	}
	return out, more, nil
}
func (h *SettingValuesHandler) adminSettingIdentity(ctx context.Context, store userstore.UserStore, req SettingIdentityRequest) (userstore.SettingIdentity, *APIError) {
	key, scope, err := h.keyedScopeFor(req.Key, req.Scope)
	if err != nil {
		return userstore.SettingIdentity{}, err
	}
	identity := userstore.SettingIdentity{Key: key, Scope: scope}
	if scope != settingscontract.ScopeAccount {
		identity.ProfileID = strings.TrimSpace(req.ProfileID)
		if identity.ProfileID == "" {
			return identity, fieldError(settingFieldProfileID, "A target profile is required")
		}
		profile, err := store.GetProfile(ctx, identity.ProfileID)
		if err != nil {
			return identity, apiError(500, "internal_error", "Failed to load target profile")
		}
		if profile == nil {
			return identity, apiError(404, "not_found", "Target profile not found")
		}
	}
	if scope == settingscontract.ScopeProfileClient {
		identity.ClientFamily = settingscontract.ClientFamily(req.ClientFamily)
	}
	if scope == settingscontract.ScopeProfileDevice {
		identity.DeviceID = strings.TrimSpace(req.DeviceID)
	}
	return h.completeIdentityFor(ctx, req.LibraryID, req.SeriesID, identity)
}
func (h *SettingValuesHandler) SetAdminAccountSetting(ctx context.Context, userID int, req SettingIdentityRequest, value json.RawMessage) (SettingValueView, error) {
	store, err := h.storeOf(ctx, userID)
	if err != nil {
		return SettingValueView{}, err
	}
	identity, err := h.adminSettingIdentity(ctx, store, req)
	if err != nil {
		return SettingValueView{}, err
	}
	result, err := h.writeSettingValue(ctx, store, userID, identity, value, "", req.Device)
	if err != nil {
		return SettingValueView{}, err
	}
	return result.response, nil
}
func (h *SettingValuesHandler) DeleteAdminAccountSetting(ctx context.Context, userID int, req SettingIdentityRequest) error {
	store, err := h.storeOf(ctx, userID)
	if err != nil {
		return err
	}
	identity, err := h.adminSettingIdentity(ctx, store, req)
	if err != nil {
		return err
	}
	if identity.Key == settingskeys.NavShortcuts {
		return fieldError(settingFieldKey, adminNavigationShortcutRepairMessage)
	}
	if err := h.clearSettingValue(ctx, store, userID, identity); err != nil {
		return err
	}
	return nil
}

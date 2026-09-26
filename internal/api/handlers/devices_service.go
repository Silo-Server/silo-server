package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type DeviceSettingsActor struct {
	UserID        int
	ProfileID     string
	VerifyProfile func(string) error
}

func (h *DeviceHandler) deviceSettingsStore(ctx context.Context, actor DeviceSettingsActor, target string, household bool) (userstore.DeviceSettingsStore, error) {
	if actor.UserID == 0 {
		return nil, apiError(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if strings.TrimSpace(actor.ProfileID) == "" {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Profile is required")
	}
	store, err := h.storeProvider.ForUser(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	if household || (target != "" && target != actor.ProfileID) {
		verify := actor.VerifyProfile
		if verify == nil {
			verify = func(string) error { return access.ErrProfileUnverified }
		}
		allowed, err := canManageHouseholdAs(ctx, store, actor.ProfileID, verify)
		if errors.Is(err, access.ErrProfileUnverified) {
			return nil, apiError(http.StatusForbidden, "forbidden", "Verify the primary profile PIN")
		}
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, apiError(http.StatusForbidden, "forbidden", "Household device management requires the primary profile or admin access")
		}
		if target != "" && target != actor.ProfileID {
			profile, err := store.GetProfile(ctx, target)
			if err != nil {
				return nil, err
			}
			if profile == nil {
				return nil, apiError(http.StatusNotFound, "not_found", "Profile not found")
			}
		}
	}
	devices, ok := store.(userstore.DeviceSettingsStore)
	if !ok {
		return nil, apiError(http.StatusServiceUnavailable, "not_configured", "Device settings are unavailable")
	}
	return devices, nil
}

func (h *DeviceHandler) DeviceSettingsPage(ctx context.Context, actor DeviceSettingsActor, household bool, opts userstore.DevicePageOptions) ([]userstore.DeviceSettingsEntry, error) {
	store, err := h.deviceSettingsStore(ctx, actor, "", household)
	if err != nil {
		return nil, err
	}
	opts.ProfileID = actor.ProfileID
	if household {
		opts.ProfileID = ""
	}
	return store.ListDeviceSettingsPage(ctx, opts)
}

func (h *DeviceHandler) RemoveDeviceSettings(ctx context.Context, actor DeviceSettingsActor, profileID, deviceID string, forget bool) error {
	profileID = strings.TrimSpace(profileID)
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return apiError(http.StatusBadRequest, "bad_request", "A device id is required")
	}
	store, err := h.deviceSettingsStore(ctx, actor, profileID, false)
	if err != nil {
		return err
	}
	if profileID == "" {
		profileID = actor.ProfileID
	}
	keys, err := store.RemoveDeviceSettings(ctx, profileID, deviceID, forget)
	if errors.Is(err, userstore.ErrDeviceNotFound) {
		return apiError(http.StatusNotFound, "not_found", "Device not found")
	}
	if err != nil {
		return err
	}
	for _, key := range keys {
		publishUserSettingsEvent(ctx, h.EventsHub, actor.UserID, profileID, key, string(settingscontract.ScopeProfileDevice))
	}
	action := "clear_device"
	if forget {
		action = "forget_device"
	}
	auditSettingsForOther(ctx, settingsAuditRecord{Action: action, ActorProfileID: apimw.GetProfileID(ctx), TargetProfileID: profileID, TargetUserID: actor.UserID, DeviceID: deviceID, Scope: string(settingscontract.ScopeProfileDevice)})
	return nil
}

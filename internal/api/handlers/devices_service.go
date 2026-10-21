package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"

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

// MaxDeviceNameRunes bounds a device name, reported or chosen, so both fit the
// same rows and layouts.
const MaxDeviceNameRunes = 120

// DeviceUpdate changes one profile's registry entry for a device. A zero
// update changes nothing but is still authorized and must name a known device.
type DeviceUpdate struct {
	// Rename is true when the update sets or clears the custom name.
	Rename bool
	// CustomName is the name the profile chose; nil clears it, so the device
	// shows its reported name again.
	CustomName *string
}

// UpdateDevice applies an update to a device under the same authorization as
// forgetting it and returns the entry as it now stands. The reported name is
// never changed, so the device can still be identified by it.
func (h *DeviceHandler) UpdateDevice(ctx context.Context, actor DeviceSettingsActor, profileID, deviceID string, update DeviceUpdate) (userstore.DeviceSettingsEntry, error) {
	profileID = strings.TrimSpace(profileID)
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return userstore.DeviceSettingsEntry{}, apiError(http.StatusBadRequest, "bad_request", "A device id is required")
	}
	name := ""
	if update.Rename && update.CustomName != nil {
		name = deviceCustomName(*update.CustomName)
		if name == "" {
			return userstore.DeviceSettingsEntry{}, fieldError("custom_name", "A device name needs at least one visible character")
		}
	}
	store, err := h.deviceSettingsStore(ctx, actor, profileID, false)
	if err != nil {
		return userstore.DeviceSettingsEntry{}, err
	}
	if profileID == "" {
		profileID = actor.ProfileID
	}
	notFound := apiError(http.StatusNotFound, "not_found", "Device not found")
	if update.Rename {
		err = store.RenameDevice(ctx, profileID, deviceID, name)
		if errors.Is(err, userstore.ErrDeviceNotFound) {
			return userstore.DeviceSettingsEntry{}, notFound
		}
		if err != nil {
			return userstore.DeviceSettingsEntry{}, err
		}
		// Identity only: the chosen name is the user's text and stays out of logs.
		auditSettingsForOther(ctx, settingsAuditRecord{Action: "rename_device", ActorProfileID: apimw.GetProfileID(ctx), TargetProfileID: profileID, TargetUserID: actor.UserID, DeviceID: deviceID})
	}
	// A forget that lands between the update and this read leaves nothing to
	// return, which is the same answer as a device that was never there.
	rows, err := store.ListDeviceSettingsPage(ctx, userstore.DevicePageOptions{ProfileID: profileID, DeviceID: deviceID, Limit: 1})
	if err != nil {
		return userstore.DeviceSettingsEntry{}, err
	}
	if len(rows) == 0 {
		return userstore.DeviceSettingsEntry{}, notFound
	}
	return rows[0], nil
}

// deviceCustomName normalizes a chosen device name. Control characters, which
// a JSON body can carry but a header cannot (and Postgres text rejects NUL),
// and bidirectional overrides, which could make the name read as other text,
// are removed; a tab or line break counts as a space. Runs of spaces collapse
// to one, and the name is bounded like a reported one. It returns "" when no
// visible character remains, such as a name of only zero-width spaces or
// variation selectors.
func deviceCustomName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r) && unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
			return -1
		default:
			return r
		}
	}, name)
	name = clampHeaderValue(strings.Join(strings.Fields(name), " "), MaxDeviceNameRunes)
	if strings.IndexFunc(name, visibleRune) < 0 {
		return ""
	}
	return name
}

// visibleRune reports whether r draws something by itself: not a space, a
// format character such as a zero-width space, a variation selector, or
// another default-ignorable code point such as the combining grapheme joiner.
// Such characters stay inside a visible name; they only can't make one.
func visibleRune(r rune) bool {
	return !unicode.IsSpace(r) && !unicode.In(r, unicode.Cf, unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point)
}

package handlers

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// DeviceHandler serves a viewer's own device registry: the devices they watch
// on, and the settings those devices carry.
//
// This is the self-service twin of the admin device routes. The important
// difference is scoping. The store's ListDevices is account-wide by
// construction — "WHERE user_id" in Postgres, and no WHERE at all in the
// per-user SQLite backend, which keeps one database per account — so every
// read here filters to a profile in the handler. Returning the store's rows
// unfiltered would show every household member's devices to everyone.
type DeviceHandler struct {
	storeProvider userstore.UserStoreProvider

	// EventsHub, when set, receives a user_settings.changed event for every
	// key a forget or clear removed. Nil (as in tests) skips publishing.
	EventsHub *evt.Hub

	// UserRepo and ProfileTokens enable ?scope=household for the household
	// parent. Both nil means the whole-household read is simply unavailable —
	// never that it is unguarded.
	UserRepo      userLookup
	ProfileTokens *access.ProfileTokenService
}

func NewDeviceHandler(provider userstore.UserStoreProvider) *DeviceHandler {
	return &DeviceHandler{storeProvider: provider}
}

type deviceResponse struct {
	DeviceID       string `json:"device_id"`
	DeviceName     string `json:"device_name"`
	DevicePlatform string `json:"device_platform"`
	LastSeenAt     string `json:"last_seen_at"`
	ProfileID      string `json:"profile_id"`
	ProfileName    string `json:"profile_name"`
	// IsCurrentDevice marks the device this request came from, so a client can
	// say "you're here" without repeating the header-matching rule.
	IsCurrentDevice bool `json:"is_current_device"`
	// ChangedCount is how many settings this (profile, device) pair overrides.
	// It is the one number a device list needs: everything else about a device
	// is either its identity or its last-seen time.
	ChangedCount int `json:"changed_count"`
}

type deviceListResponse struct {
	Devices []deviceResponse `json:"devices"`
}

// HandleListDevices handles GET /devices.
func (h *DeviceHandler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		writeJSON(w, http.StatusOK, deviceListResponse{Devices: []deviceResponse{}})
		return
	}

	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
		return
	}

	// Household scope is opt-in and guarded. The default is this profile alone,
	// so the ordinary screen stays private by construction rather than by a
	// caller remembering to ask for less.
	household := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("scope")), "household")
	if household {
		allowed, err := canManageHousehold(r, store, h.UserRepo, h.ProfileTokens)
		if err != nil {
			writeProfileManagementPermissionError(w, err)
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "forbidden",
				"Viewing the household's devices requires the primary profile or admin access")
			return
		}
	}

	devices, err := registry.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list devices")
		return
	}

	// One pass over the stored values rather than a count query per device.
	counts, err := deviceOverrideCounts(r, store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read device settings")
		return
	}

	profileNames, err := listProfileNamesByID(r.Context(), store)
	if err != nil {
		slog.WarnContext(r.Context(), "device list profile lookup failed",
			"component", "api", "error", err)
		profileNames = map[string]string{}
	}

	currentDeviceID := deviceMetadataFromRequest(r).DeviceID
	resp := deviceListResponse{Devices: make([]deviceResponse, 0, len(devices))}
	for _, device := range devices {
		if !household && device.ProfileID != profileID {
			continue
		}
		resp.Devices = append(resp.Devices, deviceResponse{
			DeviceID:        device.DeviceID,
			DeviceName:      device.DeviceName,
			DevicePlatform:  device.DevicePlatform,
			LastSeenAt:      device.LastSeenAt,
			ProfileID:       device.ProfileID,
			ProfileName:     profileNames[device.ProfileID],
			IsCurrentDevice: device.DeviceID == currentDeviceID,
			ChangedCount:    counts[deviceKey{profileID: device.ProfileID, deviceID: device.DeviceID}],
		})
	}

	// ListDevices already orders by last_seen_at; keep that and make the tie
	// break deterministic so a client's list does not reshuffle between reads.
	sort.SliceStable(resp.Devices, func(i, j int) bool {
		if resp.Devices[i].LastSeenAt != resp.Devices[j].LastSeenAt {
			return resp.Devices[i].LastSeenAt > resp.Devices[j].LastSeenAt
		}
		return resp.Devices[i].DeviceID < resp.Devices[j].DeviceID
	})

	writeJSON(w, http.StatusOK, resp)
}

// HandleForgetDevice handles DELETE /devices/{device_id}: clear the device's
// settings and drop it from the registry.
func (h *DeviceHandler) HandleForgetDevice(w http.ResponseWriter, r *http.Request) {
	h.removeDevice(w, r, true)
}

// HandleClearDeviceSettings handles DELETE /devices/{device_id}/settings:
// return the device to the profile's own values without forgetting it.
func (h *DeviceHandler) HandleClearDeviceSettings(w http.ResponseWriter, r *http.Request) {
	h.removeDevice(w, r, false)
}

func (h *DeviceHandler) removeDevice(w http.ResponseWriter, r *http.Request, forget bool) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
		return
	}
	deviceID := strings.TrimSpace(chi.URLParam(r, "device_id"))
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A device id is required")
		return
	}
	// The household parent may forget or clear a family member's device, on the
	// same guard the settings routes use.
	if named := strings.TrimSpace(r.URL.Query().Get("profile_id")); named != "" && named != profileID {
		allowed, err := canManageHousehold(r, store, h.UserRepo, h.ProfileTokens)
		if err != nil {
			writeProfileManagementPermissionError(w, err)
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "forbidden",
				"Managing another profile's devices requires the primary profile or admin access")
			return
		}
		profile, err := store.GetProfile(r.Context(), named)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load profile")
			return
		}
		if profile == nil {
			writeError(w, http.StatusNotFound, "not_found", "Profile not found")
			return
		}
		profileID = named
	}

	// A device this profile never registered is not this caller's to remove.
	// 404 rather than 403: a 403 would confirm the id exists somewhere.
	//
	// "This profile has no trace of it" covers both a device that never existed
	// and one already forgotten, which is what makes a repeated forget answer
	// 204 rather than inventing a failure for work it already did. A device
	// belonging to *another* profile is equally traceless here, so it takes the
	// same path and learns nothing about that device's existence.
	registry, isRegistry := store.(userstore.DeviceRegistry)
	owned := false
	if isRegistry {
		exists, err := registry.DeviceExists(r.Context(), profileID, deviceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to look up device")
			return
		}
		owned = exists
	}
	keys, err := h.deviceSettingKeys(r, store, profileID, deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read device settings")
		return
	}
	if !owned && len(keys) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Device not found")
		return
	}

	if _, err := store.DeleteSettingValuesForDevice(r.Context(), profileID, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear device settings")
		return
	}
	// The legacy string-keyed device settings are a second generation of the
	// same data; clearing one without the other would leave the device
	// half-reset.
	if err := store.DeleteAllDeviceSettings(r.Context(), profileID, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear device settings")
		return
	}
	if forget && isRegistry {
		if err := registry.ForgetDevice(r.Context(), profileID, deviceID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to forget device")
			return
		}
	}

	userID := apimw.GetUserID(r.Context())
	for _, key := range keys {
		publishUserSettingsEvent(r.Context(), h.EventsHub, userID, profileID,
			key, string(settingscontract.ScopeProfileDevice))
	}
	action := "clear_device"
	if forget {
		action = "forget_device"
	}
	auditSettingsForOther(r.Context(), settingsAuditRecord{
		Action:          action,
		ActorProfileID:  actingProfileID(r.Context()),
		TargetProfileID: profileID,
		TargetUserID:    userID,
		DeviceID:        deviceID,
		Scope:           string(settingscontract.ScopeProfileDevice),
	})

	w.WriteHeader(http.StatusNoContent)
}

type deviceKey struct {
	profileID string
	deviceID  string
}

// deviceOverrideCounts counts the preferences a (profile, device) pair
// overrides. ListAllSettingValues spans the whole account, so the caller filters
// the result to the profile it is answering for.
//
// Preferences, not rows: a key the contract mirrors onto its replacement is
// stored twice for the length of the overlap window, and a badge that read
// "2 settings changed" because the viewer picked one intro-skip mode would be
// counting the compatibility copy as a second choice.
func deviceOverrideCounts(r *http.Request, store userstore.UserStore) (map[deviceKey]int, error) {
	values, err := store.ListAllSettingValues(r.Context())
	if err != nil {
		return nil, err
	}
	logical := make(map[deviceKey]map[string]struct{})
	for _, value := range values {
		if value.Scope != settingscontract.ScopeProfileDevice {
			continue
		}
		id := deviceKey{profileID: value.ProfileID, deviceID: value.DeviceID}
		keys, ok := logical[id]
		if !ok {
			keys = make(map[string]struct{})
			logical[id] = keys
		}
		keys[settingscontract.LogicalKey(value.Key)] = struct{}{}
	}
	counts := make(map[deviceKey]int, len(logical))
	for id, keys := range logical {
		counts[id] = len(keys)
	}
	return counts, nil
}

// deviceSettingKeys returns the canonical keys stored for one device, so a
// clear can publish an invalidation per key that actually moved.
func (h *DeviceHandler) deviceSettingKeys(
	r *http.Request, store userstore.UserStore, profileID, deviceID string,
) ([]string, error) {
	values, err := store.ListAllSettingValues(r.Context())
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, value := range values {
		if value.Scope != settingscontract.ScopeProfileDevice {
			continue
		}
		if value.ProfileID != profileID || value.DeviceID != deviceID {
			continue
		}
		keys = append(keys, value.Key)
	}
	return keys, nil
}

func (h *DeviceHandler) storeFor(w http.ResponseWriter, r *http.Request) (userstore.UserStore, bool) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return nil, false
	}
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return nil, false
	}
	return store, true
}

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// deviceSeenThrottle bounds how often a device's last_seen_at is refreshed from
// request traffic. Device-setting reads each registered the device (an upsert
// on a single per-device row), so a page that fetches many settings serialized
// hundreds of upserts on that row's lock. Skipping the upsert when the device
// was seen within this window removes the contention while keeping last_seen
// fresh to within the window.
const deviceSeenThrottle = 5 * time.Minute

const subtitleAppearanceSettingKey = "subtitle_appearance"
const (
	libraryPageStateSettingKey         = "ui.library_page_state"
	rememberLibraryPageStateSettingKey = "ui.remember_library_page_state"
	searchMediaScopeSettingKey         = "search.media_scope"
	dateFormatSettingKey               = "ui.date_format"
	timeFormatSettingKey               = "ui.time_format"
)

const (
	deviceIDHeader       = "X-Silo-Device-Id"
	deviceNameHeader     = "X-Silo-Device-Name"
	devicePlatformHeader = "X-Silo-Device-Platform"
)

// ServerSettingReader reads individual keys from the server_settings table.
type ServerSettingReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// SettingsHandler handles user-scoped settings endpoints.
type SettingsHandler struct {
	storeProvider  userstore.UserStoreProvider
	serverSettings ServerSettingReader
	deviceSeen     *cache.TTLCache[struct{}]
}

// NewSettingsHandler creates a new SettingsHandler.
func NewSettingsHandler(provider userstore.UserStoreProvider) *SettingsHandler {
	return &SettingsHandler{
		storeProvider: provider,
		deviceSeen:    cache.NewTTLCache[struct{}](),
	}
}

// shouldRegisterDevice reports whether the device's last_seen_at should be
// refreshed now, throttling to one upsert per deviceSeenThrottle window per
// (profile, device). It marks the device seen before returning true so a burst
// of concurrent reads collapses to a single upsert instead of contending on the
// device row.
func (h *SettingsHandler) shouldRegisterDevice(profileID, deviceID string) bool {
	if h == nil || h.deviceSeen == nil {
		return true
	}
	key := profileID + "\x00" + deviceID
	if _, seen := h.deviceSeen.Get(key); seen {
		return false
	}
	h.deviceSeen.Set(key, struct{}{}, deviceSeenThrottle)
	return true
}

// SetServerSettings configures the optional server settings reader for overlay config etc.
func (h *SettingsHandler) SetServerSettings(reader ServerSettingReader) {
	h.serverSettings = reader
}

// --- Request/Response types ---

type setSettingRequest struct {
	Value string `json:"value"`
}

type settingResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type settingsListResponse struct {
	Settings []settingResponse `json:"settings"`
}

type effectiveSettingResponse struct {
	Key               string `json:"key"`
	ProfileID         string `json:"profile_id,omitempty"`
	UserValue         string `json:"user_value,omitempty"`
	DeviceValue       string `json:"device_value,omitempty"`
	EffectiveValue    string `json:"effective_value"`
	Source            string `json:"source"`
	HasDeviceOverride bool   `json:"has_device_override"`
	DeviceID          string `json:"device_id,omitempty"`
	DeviceName        string `json:"device_name,omitempty"`
	DevicePlatform    string `json:"device_platform,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

type effectiveSettingsResponse struct {
	Settings []effectiveSettingResponse `json:"settings"`
}

type effectiveSubtitleAppearanceResponse struct {
	Key               string `json:"key"`
	ProfileID         string `json:"profile_id,omitempty"`
	GlobalValue       string `json:"global_value"`
	DeviceValue       string `json:"device_value,omitempty"`
	EffectiveValue    string `json:"effective_value"`
	HasDeviceOverride bool   `json:"has_device_override"`
	DeviceID          string `json:"device_id,omitempty"`
	DeviceName        string `json:"device_name,omitempty"`
	DevicePlatform    string `json:"device_platform,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

type settingsScope string

const (
	scopeUser   settingsScope = "user"
	scopeDevice settingsScope = "device"
)

type settingSpec struct {
	Scope        settingsScope
	DefaultValue string
	Validate     func(string) error
}

var settingsRegistry = map[string]settingSpec{
	"playback.preferred_quality": {
		Scope:        scopeDevice,
		DefaultValue: "auto",
		Validate: validateEnumSetting("playback.preferred_quality",
			"auto", "original", "2160p", "1080p-high", "1080p-medium", "1080p", "1080p-8",
			"720p-high", "720p-medium", "720p", "480p", "420p", "328p"),
	},
	"playback.audio_language": {
		Scope:        scopeDevice,
		DefaultValue: "",
		Validate: func(value string) error {
			if len(strings.TrimSpace(value)) > 32 {
				return fmt.Errorf("playback.audio_language must be 32 characters or fewer")
			}
			return nil
		},
	},
	"playback.auto_skip_intro": {
		Scope:        scopeDevice,
		DefaultValue: "false",
		Validate:     validateBoolSetting("playback.auto_skip_intro"),
	},
	"playback.auto_skip_credits": {
		Scope:        scopeDevice,
		DefaultValue: "false",
		Validate:     validateBoolSetting("playback.auto_skip_credits"),
	},
	"playback.auto_skip_recap": {
		Scope:        scopeDevice,
		DefaultValue: "false",
		Validate:     validateBoolSetting("playback.auto_skip_recap"),
	},
	"playback.auto_play_next_preview": {
		Scope:        scopeDevice,
		DefaultValue: "false",
		Validate:     validateBoolSetting("playback.auto_play_next_preview"),
	},
	"playback.auto_play_next": {
		Scope:        scopeDevice,
		DefaultValue: "true",
		Validate:     validateBoolSetting("playback.auto_play_next"),
	},
	"playback.next_up_prompt_seconds": {
		Scope:        scopeDevice,
		DefaultValue: "30",
		Validate:     validateIntRange("playback.next_up_prompt_seconds", 0, 120),
	},
	subtitleAppearanceSettingKey: {
		Scope:        scopeDevice,
		DefaultValue: "",
		Validate:     validateJSONSetting(subtitleAppearanceSettingKey),
	},
	libraryPageStateSettingKey: {
		Scope:        scopeDevice,
		DefaultValue: "",
		Validate:     validateJSONSetting(libraryPageStateSettingKey),
	},
	rememberLibraryPageStateSettingKey: {
		Scope:        scopeDevice,
		DefaultValue: "true",
		Validate:     validateBoolSetting(rememberLibraryPageStateSettingKey),
	},
	// Preferred default scope for global/catalog search. "video" keeps
	// results to movies and series; "all" mixes audiobooks in.
	searchMediaScopeSettingKey: {
		Scope:        scopeUser,
		DefaultValue: "video",
		Validate: validateEnumSetting(searchMediaScopeSettingKey,
			"all", "video", "audiobook"),
	},
	// Preferred display formats for dates and clock times. "auto" defers to
	// the client's locale default.
	dateFormatSettingKey: {
		Scope:        scopeUser,
		DefaultValue: "auto",
		Validate: validateEnumSetting(dateFormatSettingKey,
			"auto", "DD/MM/YYYY", "MM/DD/YYYY", "YYYY-MM-DD"),
	},
	timeFormatSettingKey: {
		Scope:        scopeUser,
		DefaultValue: "auto",
		Validate:     validateEnumSetting(timeFormatSettingKey, "auto", "12h", "24h"),
	},
	"player.hdr_enabled": {
		Scope:        scopeDevice,
		DefaultValue: "true",
		Validate:     validateBoolSetting("player.hdr_enabled"),
	},
	"player.dv_profile7_hdr10_fallback": {
		Scope:        scopeDevice,
		DefaultValue: "false",
		Validate:     validateBoolSetting("player.dv_profile7_hdr10_fallback"),
	},
	"player.playback_speed": {
		Scope:        scopeDevice,
		DefaultValue: "1",
		Validate:     validateFloatRange("player.playback_speed", 0.25, 3.0),
	},
	"player.audio_sync_ms": {
		Scope:        scopeDevice,
		DefaultValue: "0",
		Validate:     validateIntRange("player.audio_sync_ms", -5000, 5000),
	},
	"player.subtitle_sync_ms": {
		Scope:        scopeDevice,
		DefaultValue: "0",
		Validate:     validateIntRange("player.subtitle_sync_ms", -10000, 10000),
	},
	"player.video_gravity": {
		Scope:        scopeDevice,
		DefaultValue: "fit",
		Validate:     validateEnumSetting("player.video_gravity", "fit", "fill", "stretch"),
	},
	"player.orientation_mode": {
		Scope:        scopeDevice,
		DefaultValue: "landscapeLocked",
		Validate:     validateEnumSetting("player.orientation_mode", "landscapeLocked", "rotateFreely"),
	},
}

// --- Handler methods ---

// HandleListSettings handles GET /settings.
func (h *SettingsHandler) HandleListSettings(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	entries, err := store.ListSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list settings")
		return
	}

	resp := settingsListResponse{
		Settings: make([]settingResponse, 0, len(entries)),
	}
	for _, e := range entries {
		if !keyUsesUserScope(e.Key) {
			continue
		}
		resp.Settings = append(resp.Settings, settingResponse{
			Key:   e.Key,
			Value: e.Value,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleGetSetting handles GET /settings/{key}.
func (h *SettingsHandler) HandleGetSetting(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	key := chi.URLParam(r, "key")

	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesUserScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeUser))
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	value, err := store.GetSetting(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get setting")
		return
	}

	if value == "" {
		writeError(w, http.StatusNotFound, "not_found", "Setting not found")
		return
	}

	writeJSON(w, http.StatusOK, settingResponse{
		Key:   key,
		Value: value,
	})
}

// HandleSetSetting handles PUT /settings/{key}.
func (h *SettingsHandler) HandleSetSetting(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	key := chi.URLParam(r, "key")

	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesUserScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeUser))
		return
	}

	var req setSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := validateRegisteredSetting(key, req.Value, scopeUser); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.SetSetting(r.Context(), key, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set setting")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteSetting handles DELETE /settings/{key}.
func (h *SettingsHandler) HandleDeleteSetting(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	key := chi.URLParam(r, "key")

	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesUserScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeUser))
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.DeleteSetting(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete setting")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleGetDeviceSetting handles GET /settings/device/{key}.
func (h *SettingsHandler) HandleGetDeviceSetting(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	device := deviceMetadataFromRequest(r)

	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesDeviceScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeDevice))
		return
	}
	if device.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Device id is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	h.registerRequestDevice(r.Context(), store, profileID, device)

	value, err := store.GetDeviceSetting(r.Context(), profileID, device.DeviceID, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get device setting")
		return
	}
	if value == nil {
		writeError(w, http.StatusNotFound, "not_found", "Setting not found")
		return
	}

	writeJSON(w, http.StatusOK, settingResponse{
		Key:   key,
		Value: value.Value,
	})
}

// HandleSetDeviceSetting handles PUT /settings/device/{key}.
func (h *SettingsHandler) HandleSetDeviceSetting(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	device := deviceMetadataFromRequest(r)

	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesDeviceScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeDevice))
		return
	}
	if device.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Device id is required")
		return
	}

	var req setSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := validateRegisteredSetting(key, req.Value, scopeDevice); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.SetDeviceSetting(r.Context(), userstore.DeviceSettingEntry{
		ProfileID:      profileID,
		DeviceID:       device.DeviceID,
		DeviceName:     device.DeviceName,
		DevicePlatform: device.DevicePlatform,
		Key:            key,
		Value:          req.Value,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set device setting")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteDeviceSetting handles DELETE /settings/device/{key}.
func (h *SettingsHandler) HandleDeleteDeviceSetting(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	device := deviceMetadataFromRequest(r)

	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesDeviceScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeDevice))
		return
	}
	if device.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Device id is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	h.registerRequestDevice(r.Context(), store, profileID, device)

	if err := store.DeleteDeviceSetting(r.Context(), profileID, device.DeviceID, key); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete device setting")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleGetEffectiveSettings handles GET /settings/effective?keys=key1,key2
func (h *SettingsHandler) HandleGetEffectiveSettings(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	keysParam := strings.TrimSpace(r.URL.Query().Get("keys"))
	if keysParam == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Query parameter keys is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	device := deviceMetadataFromRequest(r)
	h.registerRequestDevice(r.Context(), store, profileID, device)
	keys := parseSettingKeys(keysParam)
	resp := effectiveSettingsResponse{
		Settings: make([]effectiveSettingResponse, 0, len(keys)),
	}
	for _, key := range keys {
		resolved, err := h.resolveEffectiveSetting(r.Context(), store, profileID, device, key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve effective settings")
			return
		}
		resp.Settings = append(resp.Settings, resolved)
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleGetEffectiveSubtitleAppearance handles GET /settings/subtitle_appearance/effective.
func (h *SettingsHandler) HandleGetEffectiveSubtitleAppearance(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	device := deviceMetadataFromRequest(r)

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	h.registerRequestDevice(r.Context(), store, profileID, device)

	resolved, err := h.resolveEffectiveSetting(r.Context(), store, profileID, device, subtitleAppearanceSettingKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get setting")
		return
	}

	resp := effectiveSubtitleAppearanceResponse{
		Key:               subtitleAppearanceSettingKey,
		ProfileID:         resolved.ProfileID,
		GlobalValue:       resolved.UserValue,
		DeviceValue:       resolved.DeviceValue,
		EffectiveValue:    resolved.EffectiveValue,
		HasDeviceOverride: resolved.HasDeviceOverride,
		DeviceID:          resolved.DeviceID,
		DeviceName:        resolved.DeviceName,
		DevicePlatform:    resolved.DevicePlatform,
		UpdatedAt:         resolved.UpdatedAt,
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleSetSubtitleAppearanceDeviceOverride handles PUT /settings/device/subtitle_appearance.
func (h *SettingsHandler) HandleSetSubtitleAppearanceDeviceOverride(w http.ResponseWriter, r *http.Request) {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("key", subtitleAppearanceSettingKey)
	h.HandleSetDeviceSetting(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx)))
}

// HandleDeleteSubtitleAppearanceDeviceOverride handles DELETE /settings/device/subtitle_appearance.
func (h *SettingsHandler) HandleDeleteSubtitleAppearanceDeviceOverride(w http.ResponseWriter, r *http.Request) {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("key", subtitleAppearanceSettingKey)
	h.HandleDeleteDeviceSetting(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx)))
}

type requestDeviceMetadata struct {
	DeviceID       string
	DeviceName     string
	DevicePlatform string
}

func activeProfileIDFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
		return "", false
	}
	return profileID, true
}

func deviceMetadataFromRequest(r *http.Request) requestDeviceMetadata {
	return requestDeviceMetadata{
		DeviceID:       clampHeaderValue(r.Header.Get(deviceIDHeader), 128),
		DeviceName:     clampHeaderValue(r.Header.Get(deviceNameHeader), 120),
		DevicePlatform: clampHeaderValue(r.Header.Get(devicePlatformHeader), 40),
	}
}

func (h *SettingsHandler) registerRequestDevice(
	ctx context.Context,
	store userstore.UserStore,
	profileID string,
	device requestDeviceMetadata,
) {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(device.DeviceID) == "" {
		return
	}
	if store == nil {
		return
	}
	if !h.shouldRegisterDevice(profileID, device.DeviceID) {
		return
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		return
	}
	if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{
		ProfileID:      profileID,
		DeviceID:       device.DeviceID,
		DeviceName:     device.DeviceName,
		DevicePlatform: device.DevicePlatform,
	}); err != nil {
		slog.Warn("failed to register request device",
			"profile_id", profileID,
			"device_id", device.DeviceID,
			"error", err,
		)
	}
}

func clampHeaderValue(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxLen {
		return value
	}
	return string(runes[:maxLen])
}

func parseSettingKeys(raw string) []string {
	parts := strings.Split(raw, ",")
	keys := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		key := strings.TrimSpace(part)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func validateRegisteredSetting(key, value string, expectedScope settingsScope) error {
	spec, ok := settingsRegistry[key]
	if !ok {
		return nil
	}
	if spec.Scope != expectedScope {
		return fmt.Errorf("%s is not a %s setting", key, expectedScope)
	}
	if spec.Validate == nil {
		return nil
	}
	return spec.Validate(value)
}

func keyUsesUserScope(key string) bool {
	spec, ok := settingsRegistry[key]
	return !ok || spec.Scope == scopeUser
}

func keyUsesDeviceScope(key string) bool {
	spec, ok := settingsRegistry[key]
	return ok && spec.Scope == scopeDevice
}

func isMigratedPlaybackSetting(key string) bool {
	switch key {
	case "playback.preferred_quality",
		"playback.audio_language",
		"playback.auto_skip_intro",
		"playback.auto_skip_credits",
		"playback.auto_play_next",
		"playback.next_up_prompt_seconds":
		return true
	default:
		return false
	}
}

func usesLegacyUserFallback(key string) bool {
	return key == subtitleAppearanceSettingKey
}

func validateEnumSetting(key string, allowed ...string) func(string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	return func(value string) error {
		if _, ok := allowedSet[value]; ok {
			return nil
		}
		return fmt.Errorf("%s must be one of %s", key, strings.Join(allowed, ", "))
	}
}

func validateBoolSetting(key string) func(string) error {
	return func(value string) error {
		if value == "true" || value == "false" {
			return nil
		}
		return fmt.Errorf("%s must be true or false", key)
	}
}

func validateIntRange(key string, min, max int) func(string) error {
	return func(value string) error {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		if parsed < min || parsed > max {
			return fmt.Errorf("%s must be between %d and %d", key, min, max)
		}
		return nil
	}
}

func validateFloatRange(key string, min, max float64) func(string) error {
	return func(value string) error {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%s must be a number", key)
		}
		if math.IsNaN(parsed) || parsed < min || parsed > max {
			return fmt.Errorf("%s must be between %g and %g", key, min, max)
		}
		return nil
	}
}

func validateJSONSetting(key string) func(string) error {
	return func(value string) error {
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return fmt.Errorf("%s must be valid JSON", key)
		}
		return nil
	}
}

func (h *SettingsHandler) resolveEffectiveSetting(
	ctx context.Context,
	store userstore.UserStore,
	profileID string,
	device requestDeviceMetadata,
	key string,
) (effectiveSettingResponse, error) {
	spec, hasSpec := settingsRegistry[key]
	resolved := effectiveSettingResponse{
		Key:            key,
		ProfileID:      profileID,
		DeviceID:       device.DeviceID,
		DeviceName:     device.DeviceName,
		DevicePlatform: device.DevicePlatform,
	}

	if hasSpec && spec.Scope == scopeDevice {
		resolved.EffectiveValue = ""
		resolved.Source = "default"
		if spec.DefaultValue != "" {
			resolved.EffectiveValue = spec.DefaultValue
		} else {
			resolved.Source = "unset"
		}
		if device.DeviceID != "" {
			override, err := store.GetDeviceSetting(ctx, profileID, device.DeviceID, key)
			if err != nil {
				return effectiveSettingResponse{}, err
			}
			if override != nil {
				resolved.DeviceValue = override.Value
				resolved.EffectiveValue = override.Value
				resolved.Source = "device"
				resolved.HasDeviceOverride = true
				resolved.DeviceName = override.DeviceName
				resolved.DevicePlatform = override.DevicePlatform
				resolved.UpdatedAt = override.UpdatedAt
				return resolved, nil
			}
			if isMigratedPlaybackSetting(key) {
				legacyValue, err := store.GetSetting(ctx, key)
				if err != nil {
					return effectiveSettingResponse{}, err
				}
				if legacyValue != "" {
					entry := userstore.DeviceSettingEntry{
						ProfileID:      profileID,
						DeviceID:       device.DeviceID,
						DeviceName:     device.DeviceName,
						DevicePlatform: device.DevicePlatform,
						Key:            key,
						Value:          legacyValue,
					}
					if err := store.SetDeviceSetting(ctx, entry); err != nil {
						return effectiveSettingResponse{}, err
					}
					resolved.DeviceValue = legacyValue
					resolved.EffectiveValue = legacyValue
					resolved.Source = "device"
					resolved.HasDeviceOverride = true
					return resolved, nil
				}
			}
		}
		if usesLegacyUserFallback(key) {
			legacyValue, err := store.GetSetting(ctx, key)
			if err != nil {
				return effectiveSettingResponse{}, err
			}
			if legacyValue != "" {
				resolved.UserValue = legacyValue
				resolved.EffectiveValue = legacyValue
				resolved.Source = "user"
				return resolved, nil
			}
		}
		return resolved, nil
	}

	userValue, err := store.GetSetting(ctx, key)
	if err != nil {
		return effectiveSettingResponse{}, err
	}
	resolved.UserValue = userValue
	resolved.EffectiveValue = userValue
	resolved.Source = "user"

	if resolved.EffectiveValue != "" {
		return resolved, nil
	}

	if hasSpec && spec.DefaultValue != "" {
		resolved.EffectiveValue = spec.DefaultValue
		resolved.Source = "default"
		return resolved, nil
	}

	resolved.Source = "unset"
	return resolved, nil
}

// overlayConfigResponse is returned by GET /settings/overlay-config.
type overlayConfigResponse struct {
	Enabled  bool   `json:"enabled"`
	Defaults string `json:"defaults,omitempty"`
}

// HandleGetOverlayConfig returns the server-wide overlay configuration.
// Available to all authenticated users (not admin-only).
func (h *SettingsHandler) HandleGetOverlayConfig(w http.ResponseWriter, r *http.Request) {
	resp := overlayConfigResponse{Enabled: true}

	if h.serverSettings != nil {
		if v, _ := h.serverSettings.Get(r.Context(), "overlays.enabled"); v == "false" {
			resp.Enabled = false
		}
		if v, _ := h.serverSettings.Get(r.Context(), "defaults.card_overlays"); v != "" {
			resp.Defaults = v
		}
	}

	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, http.StatusOK, resp)
}

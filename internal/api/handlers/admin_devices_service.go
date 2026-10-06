package handlers

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ReadAdminDevices retains the existing bounded fanout across account stores.
// It enumerates all device metadata; it does not send device or playback commands.
func (h *AdminHandler) ReadAdminDevices(ctx context.Context) ([]AdminDeviceSummaryView, error) {
	if h.userRepo == nil || h.storeProv == nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Device settings not configured")
	}

	users, err := h.userRepo.List(ctx)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list devices")
	}

	perUser := make([][]adminDeviceSummaryResponse, len(users))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, user := range users {
		g.Go(func() error {
			store, err := h.storeProv.ForUser(gctx, user.ID)
			if err != nil {
				return fmt.Errorf("user store: %w", err)
			}
			entries, err := store.ListAllDeviceSettings(gctx)
			if err != nil {
				return fmt.Errorf("list device settings: %w", err)
			}
			canonicalValues, err := store.ListAllSettingValues(gctx)
			if err != nil {
				return fmt.Errorf("list canonical setting values: %w", err)
			}
			devices, err := listRegisteredDevices(gctx, store)
			if err != nil {
				return fmt.Errorf("list devices: %w", err)
			}
			profileNames, err := listProfileNamesByID(gctx, store)
			if err != nil {
				slog.WarnContext(ctx, "admin list devices profile lookup failed", "component", "api",
					"user_id", user.ID,
					"error", err,
				)
				profileNames = map[string]string{}
			}
			perUser[i] = buildAdminDeviceSummaries(
				user.ID,
				user.Username,
				user.Email,
				entries,
				canonicalValues,
				devices,
				profileNames,
			)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		slog.ErrorContext(ctx, "admin list devices failed", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list devices")
	}

	devices := make([]adminDeviceSummaryResponse, 0)
	for _, batch := range perUser {
		devices = append(devices, batch...)
	}

	slices.SortFunc(devices, func(a, b AdminDeviceSummaryView) int {
		return cmp.Or(cmp.Compare(b.LastUpdated, a.LastUpdated), cmp.Compare(a.Username, b.Username), cmp.Compare(a.DeviceName, b.DeviceName), cmp.Compare(a.DeviceID, b.DeviceID))
	})

	return devices, nil
}

// ReadAdminDevice includes the legacy settings array for bridge compatibility.
// Canonical overrides are read separately through the settings-values service.
func (h *AdminHandler) ReadAdminDevice(ctx context.Context, userIDRawInput, deviceIDInput string) (*AdminDeviceDetailView, error) {
	if h.userRepo == nil || h.storeProv == nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Device settings not configured")
	}

	userIDRaw := strings.TrimSpace(userIDRawInput)
	userID, err := strconv.Atoi(userIDRaw)
	if err != nil || userID <= 0 {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Invalid user id")
	}
	deviceID := strings.TrimSpace(deviceIDInput)
	if deviceID == "" {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Device id is required")
	}

	user, err := h.userRepo.GetByID(ctx, userID)
	if err != nil || user == nil {
		return nil, apiError(http.StatusNotFound, "not_found", "User not found")
	}
	store, err := h.storeProv.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(500, "internal_error", "Failed to access user store")
	}
	if store == nil {
		return nil, apiError(404, "not_found", "User store not found")
	}
	entries, err := store.ListAllDeviceSettings(ctx)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load device")
	}
	canonicalValues, err := store.ListAllSettingValues(ctx)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load device")
	}
	registeredDevices, err := listRegisteredDevices(ctx, store)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load device")
	}
	profileNames, err := listProfileNamesByID(ctx, store)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list profiles")
	}

	deviceEntries := make([]userstore.DeviceSettingEntry, 0)
	for _, entry := range entries {
		if entry.DeviceID == deviceID {
			deviceEntries = append(deviceEntries, entry)
		}
	}
	deviceRegistrations := make([]userstore.DeviceEntry, 0)
	for _, entry := range registeredDevices {
		if entry.DeviceID == deviceID {
			deviceRegistrations = append(deviceRegistrations, entry)
		}
	}
	deviceCanonicalValues := make([]userstore.SettingValue, 0)
	for _, value := range canonicalValues {
		if value.Scope == settingscontract.ScopeProfileDevice && value.DeviceID == deviceID {
			deviceCanonicalValues = append(deviceCanonicalValues, value)
		}
	}
	summaries := buildAdminDeviceSummaries(
		user.ID,
		user.Username,
		user.Email,
		deviceEntries,
		deviceCanonicalValues,
		deviceRegistrations,
		profileNames,
	)
	if len(summaries) == 0 {
		return nil, apiError(http.StatusNotFound, "not_found", "Device not found")
	}

	summary := summaries[0]
	return &AdminDeviceDetailView{
		UserID:         user.ID,
		Username:       user.Username,
		Email:          user.Email,
		DeviceID:       summary.DeviceID,
		DeviceName:     summary.DeviceName,
		DevicePlatform: summary.DevicePlatform,
		OverrideCount:  summary.OverrideCount,
		ProfileCount:   summary.ProfileCount,
		Profiles:       summary.Profiles,
		LastUpdated:    summary.LastUpdated,
		Settings:       buildAdminDeviceSettingsResponse(user.ID, profileNames, deviceEntries).Settings,
	}, nil
}

// AdminUserDeviceView is one device of one account: the fleet summary plus
// when the app last registered it. LastSeenAt is the latest registration of
// the device across profiles and ProfileLastSeenAt the latest per profile, as
// RFC 3339; both are empty when the device is known only from saved settings.
type AdminUserDeviceView struct {
	AdminDeviceSummaryView
	LastSeenAt        string
	ProfileLastSeenAt map[string]string
}

// ReadAdminUserDevices lists one account's devices: every device that
// registered with the app or holds saved per-device settings. A missing
// account is auth.ErrNotFound.
func (h *AdminHandler) ReadAdminUserDevices(ctx context.Context, userID int) ([]AdminUserDeviceView, error) {
	if h.userRepo == nil || h.storeProv == nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Device settings not configured")
	}
	user, err := h.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, auth.ErrNotFound
	}
	store, err := h.storeProv.ForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("user store: %w", err)
	}
	if store == nil {
		return nil, auth.ErrNotFound
	}
	entries, err := store.ListAllDeviceSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list device settings: %w", err)
	}
	canonicalValues, err := store.ListAllSettingValues(ctx)
	if err != nil {
		return nil, fmt.Errorf("list canonical setting values: %w", err)
	}
	registered, err := listRegisteredDevices(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	profileNames, err := listProfileNamesByID(ctx, store)
	if err != nil {
		slog.WarnContext(ctx, "admin account devices profile lookup failed", "component", "api", "user_id", userID, "error", err)
		profileNames = map[string]string{}
	}
	summaries := buildAdminDeviceSummaries(user.ID, user.Username, user.Email, entries, canonicalValues, registered, profileNames)

	type seen struct {
		device   time.Time
		profiles map[string]time.Time
	}
	byDevice := make(map[string]*seen)
	for _, entry := range registered {
		deviceID := strings.TrimSpace(entry.DeviceID)
		at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(entry.LastSeenAt))
		if deviceID == "" || err != nil {
			continue
		}
		current, ok := byDevice[deviceID]
		if !ok {
			current = &seen{profiles: make(map[string]time.Time)}
			byDevice[deviceID] = current
		}
		if at.After(current.device) {
			current.device = at
		}
		if profileID := strings.TrimSpace(entry.ProfileID); profileID != "" && at.After(current.profiles[profileID]) {
			current.profiles[profileID] = at
		}
	}
	out := make([]AdminUserDeviceView, 0, len(summaries))
	for _, summary := range summaries {
		view := AdminUserDeviceView{AdminDeviceSummaryView: summary, ProfileLastSeenAt: map[string]string{}}
		if current, ok := byDevice[summary.DeviceID]; ok {
			view.LastSeenAt = current.device.UTC().Format(time.RFC3339Nano)
			for profileID, at := range current.profiles {
				view.ProfileLastSeenAt[profileID] = at.UTC().Format(time.RFC3339Nano)
			}
		}
		out = append(out, view)
	}
	return out, nil
}

func (h *AdminHandler) AdminDevicesAvailable() bool {
	return h != nil && h.userRepo != nil && h.storeProv != nil
}

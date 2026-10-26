package handlers

import (
	"context"
	"log/slog"
	"strings"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// DeviceSightings registers the devices a profile uses in the device registry
// (user_devices) from ordinary request traffic: settings reads and writes, and
// playback starts. Registration is what makes a device appear in the device
// lists and advances its last_seen_at.
//
// One instance is shared by every handler that records sightings, so the
// throttle is shared across surfaces for each (profile, device). It is
// best-effort: concurrent requests can each upsert before any marks the device.
// The throttle is per process; each replica refreshes independently.
//
// Callers record only the caller's own device for the caller's own profile.
// A request that addresses another device or acts for another profile says
// nothing about which device that profile is holding. An impersonation session
// records nothing: the device belongs to the administrator viewing as the
// account, not to the account's profile.
type DeviceSightings struct {
	seen *cache.TTLCache[struct{}]
}

// NewDeviceSightings returns an empty sightings recorder.
func NewDeviceSightings() *DeviceSightings {
	return &DeviceSightings{seen: cache.NewTTLCache[struct{}]()}
}

func sightingKey(profileID, deviceID string) string {
	return profileID + "\x00" + deviceID
}

// due reports whether the device should be registered now. It marks the device
// seen before returning true. The check and mark are not atomic, so every
// concurrent request that observes a miss may perform an idempotent upsert.
func (s *DeviceSightings) due(profileID, deviceID string) bool {
	if s.seen == nil {
		return true
	}
	key := sightingKey(profileID, deviceID)
	if _, seen := s.seen.Get(key); seen {
		return false
	}
	s.seen.Set(key, struct{}{}, deviceSeenThrottle)
	return true
}

// forget clears the throttle mark after a failed registration, so the next
// request from the device retries instead of waiting out the window.
func (s *DeviceSightings) forget(profileID, deviceID string) {
	if s.seen != nil {
		s.seen.Invalidate(sightingKey(profileID, deviceID))
	}
}

// Record registers the device for the profile in store when the best-effort
// throttle is due. It never fails the request: a registry error is logged.
// A nil recorder, an empty profile or device id, an impersonation session, or
// a store without a device registry records nothing.
func (s *DeviceSightings) Record(
	ctx context.Context, store userstore.UserStore, profileID string, device DeviceMetadata,
) {
	if store == nil {
		return
	}
	s.record(ctx, profileID, device, func() (userstore.UserStore, error) { return store, nil })
}

// RecordFor is Record for a caller that has not resolved the account's store;
// the store is resolved only when a registration is due.
func (s *DeviceSightings) RecordFor(
	ctx context.Context, provider userstore.UserStoreProvider, userID int, profileID string, device DeviceMetadata,
) {
	if provider == nil || userID <= 0 {
		return
	}
	s.record(ctx, profileID, device, func() (userstore.UserStore, error) { return provider.ForUser(ctx, userID) })
}

func (s *DeviceSightings) record(
	ctx context.Context, profileID string, device DeviceMetadata, storeOf func() (userstore.UserStore, error),
) {
	profileID = strings.TrimSpace(profileID)
	if s == nil || profileID == "" || strings.TrimSpace(device.DeviceID) == "" {
		return
	}
	if claims := apimw.GetClaims(ctx); claims != nil && claims.ImpersonatorUserID != nil {
		return
	}
	if !s.due(profileID, device.DeviceID) {
		return
	}
	store, err := storeOf()
	if err != nil {
		s.forget(profileID, device.DeviceID)
		slog.WarnContext(ctx, "failed to access user store to register device", "component", "api",
			"profile_id", profileID, "device_id", device.DeviceID, "error", logredact.SanitizeText(err.Error()))
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
		s.forget(profileID, device.DeviceID)
		slog.WarnContext(ctx, "failed to register request device", "component", "api",
			"profile_id", profileID, "device_id", device.DeviceID, "error", logredact.SanitizeText(err.Error()))
	}
}

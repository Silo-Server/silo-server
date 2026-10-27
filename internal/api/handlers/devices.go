package handlers

import (
	"context"
	"log/slog"
	"slices"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
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

	// Connections, when set, deletes a device's playback connection rows
	// with its last registration. Nil (as in tests) leaves them to retention.
	Connections *connectionstore.Store
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

// forgetPlaybackConnections deletes the playback connection rows of each named
// device that no profile on the account registers any more. Callers run it
// after the deletion of a registration commits, so of two concurrent deletions
// the one that checks last sees both and deletes the rows. The registry is the
// account's user store, Postgres or SQLite. A failure is only logged: the
// registration is already gone, and retention removes the rows.
func forgetPlaybackConnections(ctx context.Context, connections *connectionstore.Store, registry userstore.DeviceRegistry, userID int, devices []string) {
	if connections == nil || registry == nil || len(devices) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	registered, err := registry.ListDevices(ctx)
	if err == nil {
		var unregistered []string
		for _, device := range devices {
			if !slices.ContainsFunc(registered, func(entry userstore.DeviceEntry) bool { return entry.DeviceID == device }) {
				unregistered = append(unregistered, device)
			}
		}
		err = connections.DeleteDevices(ctx, userID, unregistered)
	}
	if err != nil {
		slog.WarnContext(ctx, "playback connection cleanup failed after device removal", "component", "api", "user_id", userID, "error", err)
	}
}

type deviceKey struct {
	profileID string
	deviceID  string
}

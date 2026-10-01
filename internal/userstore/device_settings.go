package userstore

import (
	"context"
	"errors"
)

var ErrDeviceNotFound = errors.New("device not found")

// DevicePosition retains the store timestamp's full precision for continuation.
type DevicePosition struct {
	LastSeenAt string
	ProfileID  string
	DeviceID   string
}

type DevicePageOptions struct {
	// Empty ProfileID selects the household; the application must authorize it.
	ProfileID string
	Limit     int
	After     *DevicePosition
}

func (o DevicePageOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 201 {
		return errors.New("invalid device page limit")
	}
	if o.After != nil && (o.After.LastSeenAt == "" || o.After.ProfileID == "" || o.After.DeviceID == "") {
		return errors.New("invalid device position")
	}
	return nil
}

type DeviceSettingsEntry struct {
	DeviceEntry
	ProfileName  string
	ChangedCount int
}

// DeviceSettingsStore bounds registry reads and atomically removes both settings
// generations, optionally forgetting the registry. Returned keys were deleted by
// the committed mutation and are used for settings invalidation.
type DeviceSettingsStore interface {
	ListDeviceSettingsPage(context.Context, DevicePageOptions) ([]DeviceSettingsEntry, error)
	RemoveDeviceSettings(context.Context, string, string, bool) ([]string, error)
}

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
	// DeviceID, when set, narrows the page to that device's rows.
	DeviceID string
	Limit    int
	After    *DevicePosition
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
	// CustomName is the name the profile chose for the device; empty when it
	// chose none. DeviceName stays the name the client reports.
	CustomName   string
	ProfileName  string
	ChangedCount int
}

// DeviceSettingsStore bounds registry reads and atomically removes both settings
// generations, optionally forgetting the registry. Returned keys were deleted by
// the committed mutation and are used for settings invalidation.
type DeviceSettingsStore interface {
	ListDeviceSettingsPage(context.Context, DevicePageOptions) ([]DeviceSettingsEntry, error)
	RemoveDeviceSettings(context.Context, string, string, bool) ([]string, error)
	// RenameDevice sets the custom name of one profile's registry row for a
	// device; an empty name clears it. It returns ErrDeviceNotFound when the
	// profile has no registry row for the device. Registration never writes
	// the custom name, so it survives the device being seen again.
	RenameDevice(ctx context.Context, profileID, deviceID, name string) error
}

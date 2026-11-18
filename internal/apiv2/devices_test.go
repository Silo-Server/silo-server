package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fakeDeviceSettings struct {
	calls                       int
	actor                       handlers.DeviceSettingsActor
	household                   bool
	options                     userstore.DevicePageOptions
	targetProfile, targetDevice string
	forget                      bool
	update                      handlers.DeviceUpdate
}

func (f *fakeDeviceSettings) DeviceSettingsPage(_ context.Context, a handlers.DeviceSettingsActor, h bool, o userstore.DevicePageOptions) ([]userstore.DeviceSettingsEntry, error) {
	f.calls++
	f.actor = a
	f.household = h
	f.options = o
	if o.After != nil {
		return []userstore.DeviceSettingsEntry{}, nil
	}
	return []userstore.DeviceSettingsEntry{
		{DeviceEntry: userstore.DeviceEntry{ProfileID: a.ProfileID, DeviceID: "d-1", DeviceName: "Apple TV", LastSeenAt: "2026-01-02T03:04:05.123456Z"}, CustomName: "Living Room", ProfileName: "Viewer", ChangedCount: 1},
		{DeviceEntry: userstore.DeviceEntry{ProfileID: a.ProfileID, DeviceID: "d-2", LastSeenAt: "2026-01-02T03:04:05.123456Z"}},
	}, nil
}
func (f *fakeDeviceSettings) RemoveDeviceSettings(_ context.Context, a handlers.DeviceSettingsActor, p, d string, forget bool) error {
	f.calls++
	f.actor = a
	f.targetProfile = p
	f.targetDevice = d
	f.forget = forget
	if d == "missing" {
		return &handlers.APIError{Status: 404, Code: "not_found", Message: "Device not found"}
	}
	return nil
}
func (f *fakeDeviceSettings) UpdateDevice(_ context.Context, a handlers.DeviceSettingsActor, p, d string, u handlers.DeviceUpdate) (userstore.DeviceSettingsEntry, error) {
	f.calls++
	f.actor = a
	f.targetProfile = p
	f.targetDevice = d
	f.update = u
	if d == "missing" {
		return userstore.DeviceSettingsEntry{}, &handlers.APIError{Status: 404, Code: "not_found", Message: "Device not found"}
	}
	entry := userstore.DeviceSettingsEntry{DeviceEntry: userstore.DeviceEntry{ProfileID: p, DeviceID: d, DeviceName: "Apple TV", DevicePlatform: "tvOS", LastSeenAt: "2026-01-02T03:04:05.123456Z"}, ProfileName: "Family"}
	if u.Rename && u.CustomName != nil {
		entry.CustomName = *u.CustomName
	}
	return entry, nil
}

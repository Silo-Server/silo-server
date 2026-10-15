package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	opListDevices         = "listDevices"
	opClearDeviceSettings = "clearDeviceSettings"
	opForgetDevice        = "forgetDevice"
	opUpdateDevice        = "updateDevice"
)

type DeviceSettingsService interface {
	DeviceSettingsPage(context.Context, handlers.DeviceSettingsActor, bool, userstore.DevicePageOptions) ([]userstore.DeviceSettingsEntry, error)
	RemoveDeviceSettings(context.Context, handlers.DeviceSettingsActor, string, string, bool) error
	UpdateDevice(context.Context, handlers.DeviceSettingsActor, string, string, handlers.DeviceUpdate) error
}

type DeviceSettings struct {
	DeviceID        string  `json:"device_id"`
	DeviceName      string  `json:"device_name" doc:"Name the device reports for itself"`
	CustomName      string  `json:"custom_name,omitempty" doc:"Name the profile gave the device; show it in place of device_name. Absent when none was given" example:"Living Room"`
	DevicePlatform  string  `json:"device_platform"`
	LastSeenAt      Instant `json:"last_seen_at"`
	ProfileID       ID      `json:"profile_id"`
	ProfileName     string  `json:"profile_name"`
	IsCurrentDevice bool    `json:"is_current_device"`
	ChangedCount    int     `json:"changed_count" minimum:"0"`
}

type DeviceSettingsListInput struct {
	LimitParam
	Cursor       string `query:"cursor"`
	Scope        string `query:"scope" enum:"profile,household" default:"profile"`
	DeviceHeader string `header:"X-Silo-Device-Id" maxLength:"128"`
}

type DeviceSettingsCollection struct{ Collection[DeviceSettings] }
type DeviceSettingsListOutput struct{ Body DeviceSettingsCollection }
type DeviceSettingsRemoveInput struct {
	DeviceID  string `path:"device_id" minLength:"1" maxLength:"128"`
	ProfileID ID     `query:"profile_id" doc:"Another profile on the account; requires household management authority"`
}
type DeviceUpdateInput struct {
	DeviceID  string `path:"device_id" minLength:"1" maxLength:"128"`
	ProfileID ID     `query:"profile_id" doc:"Another profile on the account; requires household management authority"`
	Body      DeviceUpdate
}

// DeviceUpdate is the updateDevice body. Omitted members are unchanged.
type DeviceUpdate struct {
	CustomName Patch[string] `json:"custom_name,omitzero" minLength:"1" maxLength:"120" doc:"Name to show for the device in place of the name it reports. Control characters and bidirectional overrides are removed and runs of spaces collapse; a name with no visible character is rejected. null removes it. The reported device_name is kept" example:"Living Room"`
}

func registerDeviceSettings(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/devices", opListDevices, "devices", "List settings devices for the acting profile, or explicitly authorized household."), Class: ClassProfileScoped, ServiceBacked: true}, func(ctx context.Context, in *DeviceSettingsListInput) (*DeviceSettingsListOutput, error) {
		return reg.listDevices(ctx, cursors, in)
	})
	for _, command := range []struct {
		path, id string
		forget   bool
	}{{"/devices/{device_id}", opForgetDevice, true}, {"/devices/{device_id}/settings", opClearDeviceSettings, false}} {
		op := humaOp(http.MethodDelete, Prefix+command.path, command.id, "devices", "Clear device settings; forgetting additionally removes its registry entry. Login sessions remain active.")
		op.DefaultStatus = http.StatusNoContent
		Register(reg, Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}, func(ctx context.Context, in *DeviceSettingsRemoveInput) (*struct{}, error) {
			if reg.deps.DeviceSettings == nil {
				return nil, unavailable("device settings")
			}
			actor, p := deviceSettingsActor(ctx)
			if p != nil {
				return nil, p
			}
			if err := reg.deps.DeviceSettings.RemoveDeviceSettings(ctx, actor, string(in.ProfileID), in.DeviceID, command.forget); err != nil {
				return nil, fieldProblem(err)
			}
			return nil, nil
		})
	}
	update := humaOp(http.MethodPatch, Prefix+"/devices/{device_id}", opUpdateDevice, "devices", "Update a device's entry for the acting profile, or another household profile; omitted members are unchanged. Settings and login sessions are untouched.")
	update.DefaultStatus = http.StatusNoContent
	// The name converges: repeating the request leaves the same row.
	Register(reg, Operation{Operation: update, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}, reg.updateDevice)
}

func (reg *Registry) updateDevice(ctx context.Context, in *DeviceUpdateInput) (*struct{}, error) {
	if reg.deps.DeviceSettings == nil {
		return nil, unavailable("device settings")
	}
	actor, p := deviceSettingsActor(ctx)
	if p != nil {
		return nil, p
	}
	update := handlers.DeviceUpdate{Rename: in.Body.CustomName.Present}
	if in.Body.CustomName.Present && !in.Body.CustomName.Null {
		update.CustomName = &in.Body.CustomName.Value
	}
	if err := reg.deps.DeviceSettings.UpdateDevice(ctx, actor, string(in.ProfileID), in.DeviceID, update); err != nil {
		return nil, fieldProblem(err)
	}
	return nil, nil
}

func deviceSettingsActor(ctx context.Context) (handlers.DeviceSettingsActor, *Problem) {
	userID, profileID, p := viewerIdentity(ctx)
	return handlers.DeviceSettingsActor{UserID: userID, ProfileID: profileID, VerifyProfile: verifyHouseholdProfile(ctx)}, p
}

func (reg *Registry) listDevices(ctx context.Context, cursors *Cursors, in *DeviceSettingsListInput) (*DeviceSettingsListOutput, error) {
	if reg.deps.DeviceSettings == nil {
		return nil, unavailable("device settings")
	}
	actor, p := deviceSettingsActor(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{OperationID: opListDevices, Security: strconv.Itoa(actor.UserID) + "/" + actor.ProfileID + "/" + viewerScopeDigest(ctx), Filter: in.Scope, Sort: "-last_seen_at,profile_id,device_id", Tiebreaker: "profile_id,device_id"}
	var after *userstore.DevicePosition
	if in.Cursor != "" {
		after = new(userstore.DevicePosition)
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	rows, err := reg.deps.DeviceSettings.DeviceSettingsPage(ctx, actor, in.Scope == "household", userstore.DevicePageOptions{Limit: in.Limit + 1, After: after})
	if err != nil {
		return nil, fieldProblem(err)
	}
	next := ""
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		next, err = cursors.Encode(scope, userstore.DevicePosition{LastSeenAt: last.LastSeenAt, ProfileID: last.ProfileID, DeviceID: last.DeviceID})
		if err != nil {
			return nil, serviceProblem(err)
		}
	}
	items := make([]DeviceSettings, 0, len(rows))
	current := handlers.NewDeviceMetadata(in.DeviceHeader, "", "").DeviceID
	for _, row := range rows {
		seen, p := storeInstant(row.LastSeenAt)
		if p != nil {
			return nil, p
		}
		items = append(items, DeviceSettings{DeviceID: row.DeviceID, DeviceName: row.DeviceName, CustomName: row.CustomName, DevicePlatform: row.DevicePlatform, LastSeenAt: seen, ProfileID: ID(row.ProfileID), ProfileName: row.ProfileName, ChangedCount: row.ChangedCount, IsCurrentDevice: row.DeviceID == current})
	}
	return &DeviceSettingsListOutput{Body: DeviceSettingsCollection{Collection: Paginated(items, next)}}, nil
}

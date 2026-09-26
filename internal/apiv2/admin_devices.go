package apiv2

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminDeviceService interface {
	AdminDevicesAvailable() bool
	ReadAdminDevices(context.Context) ([]handlers.AdminDeviceSummaryView, error)
	ReadAdminDevice(context.Context, string, string) (*handlers.AdminDeviceDetailView, error)
}
type AdminDeviceProfile struct {
	ProfileID     ID              `json:"profile_id"`
	ProfileName   string          `json:"profile_name"`
	OverrideCount int             `json:"override_count"`
	LastUpdated   NullableInstant `json:"last_updated"`
}
type AdminDeviceMetadata struct {
	UserID         ID                   `json:"user_id"`
	Username       string               `json:"username"`
	Email          string               `json:"email"`
	DeviceID       string               `json:"device_id"`
	DeviceName     string               `json:"device_name"`
	DevicePlatform string               `json:"device_platform"`
	OverrideCount  int                  `json:"override_count"`
	ProfileCount   int                  `json:"profile_count"`
	Profiles       []AdminDeviceProfile `json:"profiles"`
	LastUpdated    NullableInstant      `json:"last_updated"`
}
type AdminLegacyDeviceSetting struct {
	UserID         ID              `json:"user_id"`
	ProfileID      ID              `json:"profile_id"`
	ProfileName    string          `json:"profile_name,omitempty"`
	DeviceID       string          `json:"device_id"`
	DeviceName     string          `json:"device_name"`
	DevicePlatform string          `json:"device_platform"`
	Key            string          `json:"key"`
	Value          string          `json:"value"`
	UpdatedAt      NullableInstant `json:"updated_at"`
}
type AdminDeviceDetail struct {
	AdminDeviceMetadata
	Settings []AdminLegacyDeviceSetting `json:"settings" doc:"Legacy compatibility rows only. Read canonical overrides through the administrator settings-values operations."`
}
type AdminDevicesInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}
type AdminDeviceInput struct {
	UserID   ID     `path:"user_id"`
	DeviceID string `path:"device_id" minLength:"1" maxLength:"256"`
}
type AdminDevicesOutput struct {
	Body Collection[AdminDeviceMetadata]
}
type AdminDeviceOutput struct{ Body AdminDeviceDetail }
type AdminDeviceCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminDeviceCapabilitiesOutputBody
}

type AdminDeviceCapabilitiesOutputBody struct {
	Capability
	Available bool `json:"available"`
}

// postgresTimestampLayouts covers what `timestamptz::text` and `timestamp::text`
// render. Stores normalize device timestamps to RFC 3339 before they reach the
// API; these layouts only keep an older raw value readable instead of dropping
// it on the floor.
var postgresTimestampLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05.999999999",
}

func deviceMetadataInstant(raw string) NullableInstant {
	if at := instantOfRFC3339(&raw); at != nil {
		return NullableInstant{Valid: true, Time: *at}
	}
	for _, layout := range postgresTimestampLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return NullableInstant{Valid: true, Time: NewInstant(t.UTC())}
		}
	}
	return NullableInstant{}
}
func adminDeviceMetadataOf(v handlers.AdminDeviceSummaryView) AdminDeviceMetadata {
	out := AdminDeviceMetadata{UserID: IDFromInt(int64(v.UserID)), Username: v.Username, Email: v.Email, DeviceID: v.DeviceID, DeviceName: v.DeviceName, DevicePlatform: v.DevicePlatform, OverrideCount: v.OverrideCount, ProfileCount: v.ProfileCount, LastUpdated: deviceMetadataInstant(v.LastUpdated), Profiles: make([]AdminDeviceProfile, 0, len(v.Profiles))}
	for _, p := range v.Profiles {
		out.Profiles = append(out.Profiles, AdminDeviceProfile{ProfileID: ID(p.ProfileID), ProfileName: p.ProfileName, OverrideCount: p.OverrideCount, LastUpdated: deviceMetadataInstant(p.LastUpdated)})
	}
	return out
}

type adminDevicePosition struct {
	Updated, Username, Name, Device string
	User                            int
}

func devicePosition(v handlers.AdminDeviceSummaryView) adminDevicePosition {
	return adminDevicePosition{v.LastUpdated, v.Username, v.DeviceName, v.DeviceID, v.UserID}
}
func compareDevicePosition(a, b adminDevicePosition) int {
	return cmp.Or(cmp.Compare(b.Updated, a.Updated), cmp.Compare(a.Username, b.Username), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Device, b.Device), cmp.Compare(a.User, b.User))
}
func registerAdminDevices(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := func(path, id string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/devices"+path, id, "admin", "Read device registration and settings metadata across account stores."), Class: ClassActingAdmin, ServiceBacked: true}
	}
	Register(reg, op("/capabilities", "getAdminDeviceCapabilities"), func(_ context.Context, _ *CapabilityInput) (*AdminDeviceCapabilitiesOutput, error) {
		out := new(AdminDeviceCapabilitiesOutput)
		out.Body.Available = reg.deps.AdminDevices != nil && reg.deps.AdminDevices.AdminDevicesAvailable()
		return out, nil
	})
	Register(reg, op("", "listAdminDevices"), func(ctx context.Context, in *AdminDevicesInput) (*AdminDevicesOutput, error) {
		if reg.deps.AdminDevices == nil || !reg.deps.AdminDevices.AdminDevicesAvailable() {
			return nil, unavailable("administrator devices")
		}
		scope := CursorScope{OperationID: "listAdminDevices", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: strconv.Itoa(in.Limit), Sort: "last_updated-desc,username,device_name,device_id", Tiebreaker: "user_id"}
		var after adminDevicePosition
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := reg.deps.AdminDevices.ReadAdminDevices(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		rows = slices.Clone(rows)
		slices.SortFunc(rows, func(a, b handlers.AdminDeviceSummaryView) int {
			return compareDevicePosition(devicePosition(a), devicePosition(b))
		})
		items := make([]AdminDeviceMetadata, 0, in.Limit)
		next := ""
		var last adminDevicePosition
		for _, row := range rows {
			pos := devicePosition(row)
			if in.Cursor != "" && compareDevicePosition(pos, after) <= 0 {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, last)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			items = append(items, adminDeviceMetadataOf(row))
			last = pos
		}
		return &AdminDevicesOutput{Body: Paginated(items, next)}, nil
	})
	Register(reg, op("/{user_id}/{device_id}", "getAdminDevice"), func(ctx context.Context, in *AdminDeviceInput) (*AdminDeviceOutput, error) {
		if reg.deps.AdminDevices == nil || !reg.deps.AdminDevices.AdminDevicesAvailable() {
			return nil, unavailable("administrator devices")
		}
		if _, p := in.UserID.positive("path.user_id"); p != nil {
			return nil, p
		}
		v, err := reg.deps.AdminDevices.ReadAdminDevice(ctx, string(in.UserID), in.DeviceID)
		if err != nil {
			return nil, serviceProblem(err)
		}
		if v == nil {
			return nil, NewProblem(TypeNotFound, "Device not found")
		}
		out := AdminDeviceDetail{AdminDeviceMetadata: adminDeviceMetadataOf(handlers.AdminDeviceSummaryView{UserID: v.UserID, Username: v.Username, Email: v.Email, DeviceID: v.DeviceID, DeviceName: v.DeviceName, DevicePlatform: v.DevicePlatform, OverrideCount: v.OverrideCount, ProfileCount: v.ProfileCount, Profiles: v.Profiles, LastUpdated: v.LastUpdated}), Settings: make([]AdminLegacyDeviceSetting, 0, len(v.Settings))}
		for _, s := range v.Settings {
			out.Settings = append(out.Settings, AdminLegacyDeviceSetting{UserID: IDFromInt(int64(s.UserID)), ProfileID: ID(s.ProfileID), ProfileName: s.ProfileName, DeviceID: s.DeviceID, DeviceName: s.DeviceName, DevicePlatform: s.DevicePlatform, Key: s.Key, Value: s.Value, UpdatedAt: deviceMetadataInstant(s.UpdatedAt)})
		}
		return &AdminDeviceOutput{Body: out}, nil
	})
}

func (c AdminDeviceCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}

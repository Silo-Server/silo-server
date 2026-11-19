package apiv2

import (
	"context"
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminDevices struct {
	calls        int
	user, device string
}

func (f *fakeAdminDevices) ReadAdminDevices(context.Context) ([]handlers.AdminDeviceSummaryView, error) {
	f.calls++
	return []handlers.AdminDeviceSummaryView{{UserID: 2, Username: "same", DeviceID: "shared", DeviceName: "Screen", LastUpdated: "2026-09-01T01:02:03.123456Z"}, {UserID: 1, Username: "same", DeviceID: "shared", DeviceName: "Screen", LastUpdated: "2026-09-01T01:02:03.123456Z"}}, nil
}
func (f *fakeAdminDevices) ReadAdminDevice(_ context.Context, user, device string) (*handlers.AdminDeviceDetailView, error) {
	f.calls++
	f.user = user
	f.device = device
	var view handlers.AdminDeviceDetailView
	err := json.Unmarshal([]byte(`{"user_id":2,"device_id":"screen/one","profiles":[{"profile_id":"child","profile_name":"Child","override_count":1,"last_updated":"2026-09-01T01:02:03Z"}],"settings":[{"user_id":2,"profile_id":"child","key":"legacy","value":"true","updated_at":"2026-09-01T01:02:03Z"}]}`), &view)
	return &view, err
}
func adminDeviceFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_devices", operationID: "listAdminDevices", method: "GET", path: Prefix + "/admin/devices", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CollectionAdminDeviceMetadata", scenario: "Account/device composite identity distinguishes a shared device ID; absent profile metadata is an empty array."},
		{name: "admin_device", operationID: "getAdminDevice", method: "GET", path: Prefix + "/admin/devices/2/screen%2Fone", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminDeviceDetail", scenario: "Device detail preserves profile identities and legacy compatibility settings; canonical overrides use the settings-values API."},
	}
}

func (f *fakeAdminDevices) AdminDevicesAvailable() bool { return true }

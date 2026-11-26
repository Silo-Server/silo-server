package apiv2

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeAdminAppKeys answers the administrator key operations.
type fakeAdminAppKeys struct {
	keys auth.AdminAccountKeys
	err  error
}

func (f *fakeAdminAppKeys) AdminListAppKeys(context.Context, int) (auth.AdminAccountKeys, error) {
	return f.keys, f.err
}

func (f *fakeAdminAppKeys) AdminRevokeAppDeviceKey(context.Context, int, string) error { return f.err }

func (f *fakeAdminAppKeys) AdminRevokeAppPersonKey(context.Context, int) error { return f.err }

func (f *fakeAdminAppKeys) AdminDenyPendingAppKey(context.Context, int, string) error { return f.err }

// fixtureAdminAppKeys is an account with a person key from a provider
// session, a device it introduced, a device waiting for approval, and a
// device whose provider account is gone.
func fixtureAdminAppKeys() *fakeAdminAppKeys {
	created := fixedTime()
	used := created.Add(time.Hour)
	identity, gone := int64(7), int64(8)
	return &fakeAdminAppKeys{keys: auth.AdminAccountKeys{
		Person: &auth.AdminPersonKey{AppPersonKey: &models.AppPersonKey{KeyID: "PgujdSfBbNNyXngLWCsOgw", EnrolledIP: "127.0.0.2", CreatedAt: created,
			EnrolledVia: models.AppKeyEnrolledSession, ProviderBound: true, IdentityID: &identity}},
		Devices: []auth.AdminDeviceKey{
			{AppDeviceKey: &models.AppDeviceKey{ID: "5b1d7f0e-6a57-4d0c-8a59-0f3f3a0f4a10", KeyID: "iX--ofJySUkUOsEpPo17PQ", Kind: models.DeviceKeyKindDevice,
				Name: "Sam's iPad", Platform: "ipados", Status: models.DeviceKeyStatusPending, EnrolledVia: models.AppKeyEnrolledIntroduce,
				ProviderBound: true, IdentityID: &identity, CreatedAt: created}},
			{AppDeviceKey: &models.AppDeviceKey{ID: "0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11", KeyID: "zT321tSgcB7emUH9k0B0oA", Kind: models.DeviceKeyKindDevice,
				Name: "Sam's iPhone", Platform: "ios", Status: models.DeviceKeyStatusActive, EnrolledVia: models.AppKeyEnrolledIntroduce,
				ProviderBound: true, IdentityID: &identity, EnrolledIP: "127.0.0.3", CreatedAt: created, LastUsedAt: &used}},
			{AppDeviceKey: &models.AppDeviceKey{ID: "2d4c6e8a-1b3d-4f5a-9c7e-0a1b2c3d4e5f", KeyID: "T2xkLWxhcHRvcC1rZXktMQ", Kind: models.DeviceKeyKindDevice,
				Name: "Old laptop", Platform: "macos", Status: models.DeviceKeyStatusActive, EnrolledVia: models.AppKeyEnrolledSession,
				ProviderBound: true, IdentityID: &gone, CreatedAt: created, LastUsedAt: &used}, IdentityGone: true},
		},
		AllowedBy: "sam",
	}}
}

// adminAppKeyFixtureCases pin the shapes the Users page decodes for one
// account's app keys.
func adminAppKeyFixtureCases() []fixtureCase {
	keys := "/api/v2/admin/users/2/keys"
	return []fixtureCase{
		{name: "list_admin_user_app_keys_ok", operationID: "listAdminUserAppKeys",
			scenario: "An administrator lists an account's keys: a person key from a provider session, a device waiting for approval, an active device, and a device whose provider account is gone.",
			method:   http.MethodGet, path: keys, headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminAppKeyList"},
		{name: "revoke_admin_user_device_key_ok", operationID: "revokeAdminUserDeviceKey",
			scenario: "An administrator removes one of an account's devices; its sessions end.",
			method:   http.MethodDelete, path: keys + "/devices/0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11", headers: bearer(adminToken),
			status: http.StatusNoContent},
		{name: "revoke_admin_user_person_key_ok", operationID: "revokeAdminUserPersonKey",
			scenario: "An administrator revokes an account's person key; enrolled devices keep working.",
			method:   http.MethodDelete, path: keys + "/person", headers: bearer(adminToken),
			status: http.StatusNoContent},
		{name: "deny_admin_user_pending_key_ok", operationID: "denyAdminUserPendingKey",
			scenario: "An administrator denies a device waiting for approval on an account.",
			method:   http.MethodPost, path: keys + "/pending/5b1d7f0e-6a57-4d0c-8a59-0f3f3a0f4a10/deny", body: `{}`, headers: bearer(adminToken),
			status: http.StatusNoContent},
	}
}

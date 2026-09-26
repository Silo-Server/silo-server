package apiv2

import (
	"context"
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fakeAdminAccountSettings struct {
	last   handlers.SettingIdentityRequest
	writes int
}

func (*fakeAdminAccountSettings) ContractRevision() int { return 12 }
func (*fakeAdminAccountSettings) ListAdminAccountSettings(_ context.Context, _ int, after userstore.SettingIdentity, _ int) ([]handlers.SettingValueView, bool, error) {
	key := "playback.audio_language"
	if after.Key != "" {
		key = "playback.subtitle_language"
	}
	return []handlers.SettingValueView{{Key: key, Scope: "profile", ProfileID: "p-owner", Value: json.RawMessage(`"en"`), Revision: 1}}, after.Key == "", nil
}
func (f *fakeAdminAccountSettings) SetAdminAccountSetting(_ context.Context, _ int, in handlers.SettingIdentityRequest, value json.RawMessage) (handlers.SettingValueView, error) {
	f.last = in
	f.writes++
	return handlers.SettingValueView{Key: in.Key, Scope: in.Scope, ProfileID: in.ProfileID, Value: value, Revision: 2}, nil
}
func (f *fakeAdminAccountSettings) DeleteAdminAccountSetting(_ context.Context, _ int, in handlers.SettingIdentityRequest) error {
	f.last = in
	f.writes++
	return nil
}

type fakeAdminAccountActivity struct{ positions []activitylog.IPPagePosition }

func (f *fakeAdminAccountActivity) UserIPsPage(_ context.Context, _ int, _ int, _ int, pos activitylog.IPPagePosition) ([]activitylog.UserIPEntry, bool, error) {
	f.positions = append(f.positions, pos)
	return []activitylog.UserIPEntry{{ClientIP: "127.0.0.1", FirstSeen: fixedTime(), LastSeen: fixedTime(), RequestCount: 3}}, false, nil
}
func (f *fakeAdminAccountActivity) IPUsersPage(_ context.Context, _ string, _ int, _ int, pos activitylog.IPPagePosition) ([]activitylog.IPUserEntry, bool, error) {
	f.positions = append(f.positions, pos)
	return []activitylog.IPUserEntry{{UserID: 7, Username: "sample", FirstSeen: fixedTime(), LastSeen: fixedTime(), RequestCount: 3}}, false, nil
}
func (f *fakeAdminAPIKeys) ListAdminUserAPIKeysPage(ctx context.Context, user int, after *auth.APIKeyPageKey, limit int) ([]handlers.AdminAPIKeyListItem, bool, error) {
	rows, more, err := f.ListAdminAPIKeysPage(ctx, after, limit)
	for i := range rows {
		rows[i].UserID = user
	}
	return rows, more, err
}
func adminAccountFixtureCases() []fixtureCase {
	cases := []fixtureCase{
		{name: "admin_account_capabilities", operationID: "getAdminAccountCapabilities", method: "GET", path: "/api/v2/admin/users/capabilities", status: 200, schema: "AdminAccountCapabilitiesOutputBody"},
		{name: "admin_account_get", operationID: "getAdminUser", method: "GET", path: "/api/v2/admin/users/7", status: 200, schema: "AdminUser"},
		{name: "admin_account_create", operationID: "createAdminUser", method: "POST", path: "/api/v2/admin/users", body: `{"username":"sample","email":"sample@example.test","password":"synthetic-password","role":"user","create_default_profile":false}`, status: 201, schema: "AdminAccountCreatedBody"},
		{name: "admin_account_update", operationID: "updateAdminUser", method: "PUT", path: "/api/v2/admin/users/7", body: `{"enabled":false}`, status: 204},
		{name: "admin_account_delete", operationID: "deleteAdminUser", method: "DELETE", path: "/api/v2/admin/users/7", status: 204},
		{name: "admin_account_impersonate", operationID: "impersonateAdminUser", method: "POST", path: "/api/v2/admin/users/7/impersonate", status: 200, schema: "TokenPair"},
		{name: "admin_account_profiles", operationID: "listAdminUserProfiles", method: "GET", path: "/api/v2/admin/users/7/profiles", status: 200, schema: "CollectionAdminAccountProfile"},
		{name: "admin_account_api_keys", operationID: "listAdminUserAPIKeys", method: "GET", path: "/api/v2/admin/users/7/api-keys?limit=1", status: 200, schema: "CollectionAdminAPIKeyListItem"},
		{name: "admin_account_ips", operationID: "listAdminUserIPs", method: "GET", path: "/api/v2/admin/users/7/ips", status: 200, schema: "CollectionAdminUserIP"},
		{name: "admin_ip_users", operationID: "listAdminIPUsers", method: "GET", path: "/api/v2/admin/ips?ip=127.0.0.1", status: 200, schema: "CollectionAdminIPUser"},
		{name: "admin_account_settings", operationID: "listAdminUserSettingValues", method: "GET", path: "/api/v2/admin/users/7/settings/values?limit=1", status: 200, schema: "AdminAccountSettingsOutputBody"},
		{name: "admin_account_setting_put", operationID: "setAdminUserSettingValue", method: "PUT", path: "/api/v2/admin/users/7/settings/values/playback.audio_language?scope=profile&profile_id=p-owner", body: `{"value":"en"}`, status: 200, schema: "SettingValue"},
		{name: "admin_account_setting_delete", operationID: "deleteAdminUserSettingValue", method: "DELETE", path: "/api/v2/admin/users/7/settings/values/playback.audio_language?scope=profile&profile_id=p-owner", status: 204},
		{name: "admin_access_group_list", operationID: "listAdminAccessGroups", method: "GET", path: "/api/v2/admin/access-groups?limit=1", status: 200, schema: "CollectionAdminAccessGroupListItem"},
		{name: "admin_access_group_get", operationID: "getAdminAccessGroup", method: "GET", path: "/api/v2/admin/access-groups/7", status: 200, schema: "AdminAccessGroup"},
		{name: "admin_access_group_create", operationID: "createAdminAccessGroup", method: "POST", path: "/api/v2/admin/access-groups", body: `{"name":"Group"}`, status: 201, schema: "AdminAccessGroup"},
		{name: "admin_access_group_update", operationID: "updateAdminAccessGroup", method: "PUT", path: "/api/v2/admin/access-groups/7", body: `{"name":"Changed"}`, status: 200, schema: "AdminAccessGroup"},
		{name: "admin_access_group_delete", operationID: "deleteAdminAccessGroup", method: "DELETE", path: "/api/v2/admin/access-groups/7", status: 204},
	}
	for i := range cases {
		c := &cases[i]
		c.headers = actingRequestAdmin
		c.scenario = "Administrator account and group management with synthetic records."
		c.assertHeaders = []string{"Cache-Control"}
		if c.schema != "" {
			c.schema = "#/components/schemas/" + c.schema
			c.assertHeaders = append(c.assertHeaders, "Content-Type")
		}
		if c.operationID == "updateAdminUser" || c.operationID == "deleteAdminUser" || c.operationID == "updateAdminAccessGroup" || c.operationID == "deleteAdminAccessGroup" {
			c.headers = with(actingRequestAdmin, "If-Match", "*")
		}
	}
	return cases
}

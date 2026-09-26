package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type AdminAccountSettingsService interface {
	ListAdminAccountSettings(context.Context, int, userstore.SettingIdentity, int) ([]handlers.SettingValueView, bool, error)
	SetAdminAccountSetting(context.Context, int, handlers.SettingIdentityRequest, json.RawMessage) (handlers.SettingValueView, error)
	DeleteAdminAccountSetting(context.Context, int, handlers.SettingIdentityRequest) error
	ContractRevision() int
}
type AdminAccountSettingsInput struct {
	ID ID `path:"id"`
	LimitParam
	Cursor string `query:"cursor"`
}
type AdminAccountSettingInput struct {
	ID           ID     `path:"id"`
	Key          string `path:"key" minLength:"1" maxLength:"128"`
	Scope        string `query:"scope" required:"true" enum:"account,profile,profile_client,profile_device,profile_library,profile_series"`
	ProfileID    ID     `query:"profile_id"`
	ClientFamily string `query:"client_family" enum:"tv,mobile,tablet,desktop,web"`
	DeviceID     string `query:"device_id" maxLength:"128"`
	LibraryID    ID     `query:"library_id"`
	SeriesID     string `query:"series_id" maxLength:"128"`
}
type AdminAccountSettingWriteInput struct {
	AdminAccountSettingInput
	Body SettingValueWrite
}
type AdminAccountSettingsOutput struct {
	Body struct {
		Collection[SettingValue]
		Revision int `json:"revision"`
	}
}

func (in AdminAccountSettingInput) identity() handlers.SettingIdentityRequest {
	return handlers.SettingIdentityRequest{Key: in.Key, Scope: in.Scope, ProfileID: string(in.ProfileID), ClientFamily: in.ClientFamily, DeviceID: in.DeviceID, LibraryID: string(in.LibraryID), SeriesID: in.SeriesID}
}
func (reg *Registry) adminAccountSettingTarget(ctx context.Context, raw ID) (int, *Problem) {
	svc, p := reg.adminAccounts()
	if p != nil {
		return 0, p
	}
	if reg.deps.AdminAccountSettings == nil {
		return 0, unavailable("administrator settings")
	}
	id, p := adminAccountID(raw)
	if p != nil {
		return 0, p
	}
	if _, err := svc.GetAdminAccount(ctx, id); err != nil {
		return 0, adminAccountError(err)
	}
	return id, nil
}
func registerAdminAccountSettings(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, adminAccountOperation(http.MethodGet, "/{id}/settings/values", "listAdminUserSettingValues", false), func(ctx context.Context, in *AdminAccountSettingsInput) (*AdminAccountSettingsOutput, error) {
		id, p := reg.adminAccountSettingTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		scope := adminPolicyListScope(ctx, "listAdminUserSettingValues", strconv.Itoa(id)+"/"+strconv.Itoa(in.Limit), "key,scope,profile,client,device,library,series", "identity")
		var after userstore.SettingIdentity
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, more, err := reg.deps.AdminAccountSettings.ListAdminAccountSettings(ctx, id, after, in.Limit)
		if err != nil {
			return nil, serviceProblem(err)
		}
		values := make([]SettingValue, 0, len(rows))
		for _, row := range rows {
			value, p := settingValueOf(row)
			if p != nil {
				return nil, p
			}
			values = append(values, value)
		}
		next := ""
		if more && len(rows) > 0 {
			r := rows[len(rows)-1]
			next, err = cursors.Encode(scope, userstore.SettingIdentity{Key: r.Key, Scope: settingscontract.Scope(r.Scope), ProfileID: r.ProfileID, ClientFamily: settingscontract.ClientFamily(r.ClientFamily), DeviceID: r.DeviceID, LibraryID: r.LibraryID, SeriesID: r.SeriesID})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		out := new(AdminAccountSettingsOutput)
		out.Body.Collection = Paginated(values, next)
		out.Body.Revision = reg.deps.AdminAccountSettings.ContractRevision()
		return out, nil
	})
	put := adminAccountOperation(http.MethodPut, "/{id}/settings/values/{key}", "setAdminUserSettingValue", false)
	put.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, put, func(ctx context.Context, in *AdminAccountSettingWriteInput) (*SettingValueOutput, error) {
		id, p := reg.adminAccountSettingTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		value, err := reg.deps.AdminAccountSettings.SetAdminAccountSetting(ctx, id, in.identity(), json.RawMessage(in.Body.Value))
		if err != nil {
			return nil, serviceProblem(err)
		}
		body, p := settingValueOf(value)
		if p != nil {
			return nil, p
		}
		return &SettingValueOutput{Body: body}, nil
	})
	del := adminAccountOperation(http.MethodDelete, "/{id}/settings/values/{key}", "deleteAdminUserSettingValue", false)
	del.RetrySafety = RetrySafetyNaturalIdempotent
	del.DefaultStatus = 204
	Register(reg, del, func(ctx context.Context, in *AdminAccountSettingInput) (*struct{}, error) {
		id, p := reg.adminAccountSettingTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		if err := reg.deps.AdminAccountSettings.DeleteAdminAccountSetting(ctx, id, in.identity()); err != nil {
			return nil, serviceProblem(err)
		}
		return &struct{}{}, nil
	})
}

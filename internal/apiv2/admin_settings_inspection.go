package apiv2

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/danielgtaylor/huma/v2"
)

type AdminSettingsInspectionService interface {
	InspectAdminSettings(context.Context, bool) (map[string]string, error)
	InspectAdminSensitiveSettings(context.Context) (handlers.AdminSensitiveSettingsStatus, error)
}

// AdminSettingValues contains the extensible server settings registry's string
// values. Sensitive and machine-managed keys are excluded by the service.
type AdminSettingValues map[string]string

func (AdminSettingValues) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: &huma.Schema{Type: huma.TypeString}, Extensions: map[string]any{extExtensionBag: "admin-setting-values"}}
}

type AdminSettingValuesOutput struct {
	ETag string `header:"ETag"`
	Body AdminSettingValues
}
type AdminRestartKeys struct {
	Keys     []string `json:"keys"`
	Prefixes []string `json:"prefixes"`
}
type AdminRestartKeysOutput struct{ Body AdminRestartKeys }
type AdminSensitiveStatus struct {
	Configured   []string `json:"configured"`
	ManagedByEnv []string `json:"managed_by_env"`
}
type AdminSensitiveStatusOutput struct{ Body AdminSensitiveStatus }

func registerAdminSettingsInspection(reg *Registry) {
	op := func(path, id, summary string, backed bool) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/settings"+path, id, "admin-settings", summary), Class: ClassActingAdmin, ServiceBacked: backed}
	}
	Register(reg, op("", "getAdminStoredSettings", "Read stored administrator settings with secrets excluded.", true), func(ctx context.Context, _ *struct{}) (*AdminSettingValuesOutput, error) {
		return reg.inspectAdminSettings(ctx, false)
	})
	Register(reg, op("/effective", "getAdminEffectiveSettings", "Read active administrator settings including runtime defaults, with secrets excluded.", true), func(ctx context.Context, _ *struct{}) (*AdminSettingValuesOutput, error) {
		return reg.inspectAdminSettings(ctx, true)
	})
	Register(reg, op("/restart-keys", "getAdminRestartKeys", "Read the compiled keys and prefixes requiring a restart.", false), func(context.Context, *struct{}) (*AdminRestartKeysOutput, error) {
		return &AdminRestartKeysOutput{Body: AdminRestartKeys{Keys: NonNil(config.RestartRequiredKeys()), Prefixes: NonNil(config.RestartRequiredPrefixes())}}, nil
	})
	Register(reg, op("/sensitive-status", "getAdminSensitiveSettingsStatus", "Read configured secret key names without returning secret values.", true), reg.inspectAdminSensitiveSettings)
}
func adminSettingsInspectionProblem(err error) error {
	if problem, ok := errors.AsType[*Problem](err); ok {
		return problem
	}

	if errors.Is(err, handlers.ErrAdminSettingsUnavailable) {
		return unavailable("administrator settings")
	}
	return serviceProblem(err)
}
func (reg *Registry) inspectAdminSettings(ctx context.Context, effective bool) (*AdminSettingValuesOutput, error) {
	if reg.deps.AdminSettingsInspection == nil {
		return nil, unavailable("administrator settings")
	}
	if reg.deps.AdminSettingsWrite != nil {
		snapshot, err := reg.deps.AdminSettingsWrite.InspectAdminSettingsSnapshot(ctx)
		if err != nil {
			return nil, adminSettingsInspectionProblem(err)
		}
		tag, err := reg.adminSettingsSnapshotTag(ctx, snapshot)
		if err != nil {
			return nil, adminSettingsInspectionProblem(err)
		}
		values := snapshot.VisibleStored
		if effective {
			values = snapshot.VisibleEffective
		}
		return &AdminSettingValuesOutput{ETag: tag.String(), Body: AdminSettingValues(NonNilMap(values))}, nil
	}
	values, err := reg.deps.AdminSettingsInspection.InspectAdminSettings(ctx, effective)
	if err != nil {
		return nil, adminSettingsInspectionProblem(err)
	}
	return &AdminSettingValuesOutput{Body: AdminSettingValues(NonNilMap(values))}, nil
}
func (reg *Registry) inspectAdminSensitiveSettings(ctx context.Context, _ *struct{}) (*AdminSensitiveStatusOutput, error) {
	if reg.deps.AdminSettingsInspection == nil {
		return nil, unavailable("administrator settings")
	}
	status, err := reg.deps.AdminSettingsInspection.InspectAdminSensitiveSettings(ctx)
	if err != nil {
		return nil, adminSettingsInspectionProblem(err)
	}
	return &AdminSensitiveStatusOutput{Body: AdminSensitiveStatus{Configured: NonNil(status.Configured), ManagedByEnv: NonNil(status.ManagedByEnv)}}, nil
}

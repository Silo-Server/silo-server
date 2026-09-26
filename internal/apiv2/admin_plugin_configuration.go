package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// AdminPluginConfigurationService is the slice of *handlers.PluginHandler the
// installation mutations use: configuration write and probe, auth binding
// and task binding assignment. Every method refuses the reserved builtin row
// and reports an unknown installation as plugins.ErrInstallationNotFound.
type AdminPluginConfigurationService interface {
	SetAdminPluginConfig(context.Context, int, handlers.PluginConfigInput) error
	TestAdminPluginConfig(context.Context, int, handlers.PluginConfigInput) (handlers.PluginConnectionCheckResult, error)
	SetAdminPluginAuthBinding(context.Context, int, handlers.PluginAuthBindingInput) error
	SetAdminPluginTaskBinding(context.Context, int, string, handlers.PluginTaskBindingInput) error
}

// AdminPluginConfigWrite is one global configuration entry as the operator
// submits it. Manifest-declared secret fields left blank keep their stored
// value; a secret is removed only when named in clear_secrets.
type AdminPluginConfigWrite struct {
	Key          string          `json:"key" minLength:"1" maxLength:"256" doc:"The manifest global_config_schema key"`
	Value        PluginJSONValue `json:"value" doc:"The entry's fields; blank secret fields preserve the stored secret"`
	ClearSecrets []string        `json:"clear_secrets,omitempty" maxItems:"64" doc:"Secret fields to remove; each must be a manifest-declared secret of this key"`
}
type AdminPluginConfigInput struct {
	ID      ID `path:"id" pattern:"^[1-9][0-9]*$"`
	RawBody []byte
	Body    AdminPluginConfigWrite
}
type AdminPluginConnectionCheck struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
type AdminPluginConnectionCheckOutput struct{ Body AdminPluginConnectionCheck }

// AdminPluginAuthBindingWrite is the whole-row auth binding assignment.
type AdminPluginAuthBindingWrite struct {
	CapabilityID  string `json:"capability_id" minLength:"1" maxLength:"256"`
	Enabled       bool   `json:"enabled"`
	DisplayOrder  int    `json:"display_order" minimum:"0" maximum:"100000"`
	AutoProvision bool   `json:"auto_provision"`
	DefaultLogin  bool   `json:"default_login"`
}
type AdminPluginAuthBindingInput struct {
	ID      ID `path:"id" pattern:"^[1-9][0-9]*$"`
	RawBody []byte
	Body    AdminPluginAuthBindingWrite
}
type AdminPluginAuthBindingOutput struct {
	RestartRequired string `header:"X-Silo-Restart-Required" doc:"Always true: bindings load at server start"`
}

// AdminPluginTaskBindingWrite is the whole-row task binding assignment.
type AdminPluginTaskBindingWrite struct {
	Enabled bool            `json:"enabled"`
	Trigger PluginJSONValue `json:"trigger,omitempty" doc:"Plugin-defined trigger document; omitted stores an empty object"`
}
type AdminPluginTaskBindingInput struct {
	ID           ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	CapabilityID string `path:"capability_id" minLength:"1" maxLength:"256"`
	RawBody      []byte
	Body         AdminPluginTaskBindingWrite
}
type AdminPluginTaskBindingResult struct {
	RestartRequired bool `json:"restart_required" doc:"Always true: bindings load at server start"`
}
type AdminPluginTaskBindingOutput struct{ Body AdminPluginTaskBindingResult }

// restartRequiredHeaderValue is the fixed X-Silo-Restart-Required value:
// bindings load at server start, so every successful write requires one.
const restartRequiredHeaderValue = "true"

func adminPluginInstallationID(id ID) (int, *Problem) {
	n, p := id.positive("path.id")
	if p != nil {
		return 0, NewProblem(TypeValidationFailed, "Invalid plugin installation ID.")
	}
	return n, nil
}

// adminPluginMutationProblem maps the seam's typed errors. Validation detail
// from the plugin's own schema is the operator's feedback and is kept.
func adminPluginMutationProblem(err error) error {
	var validation *plugins.ConfigValidationError
	switch {
	case errors.Is(err, plugins.ErrInstallationNotFound):
		return NewProblem(TypeNotFound, "Plugin installation not found.")
	case errors.Is(err, handlers.ErrPluginBuiltinInstallation):
		return NewProblem(TypeConflict, "Built-in host providers cannot be modified.")
	case errors.As(err, &validation):
		return validationProblem(locationBody, codeInvalid, validation.Error())
	}
	return serviceProblem(err)
}

func registerAdminPluginConfiguration(reg *Registry) {
	op := func(method, path, id, summary string, safety RetrySafety) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/plugins/installations/{id}/"+path, id, "admin-plugins", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, RetrySafety: safety}
		o.Errors = []int{http.StatusNotFound, http.StatusConflict}
		return o
	}
	write := op("PUT", "config", "updateAdminPluginInstallationConfig", "Replace one global configuration entry after validating it against the plugin manifest, then stop the running plugin so it rebinds. Blank secret fields keep stored secrets; clear_secrets removes them. Repeating the same request converges on one stored entry.", RetrySafetyNaturalIdempotent)
	write.DefaultStatus = http.StatusNoContent
	Register(reg, write, func(ctx context.Context, in *AdminPluginConfigInput) (*struct{}, error) {
		if reg.deps.AdminPluginConfiguration == nil {
			return nil, unavailable("plugin configuration")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		if strings.TrimSpace(in.Body.Key) == "" {
			return nil, validationProblem(locationBody+".key", codeInvalid, "A configuration key is required.")
		}
		if err := reg.deps.AdminPluginConfiguration.SetAdminPluginConfig(ctx, id, handlers.PluginConfigInput{Key: in.Body.Key, Value: map[string]any(in.Body.Value), ClearSecrets: in.Body.ClearSecrets}); err != nil {
			return nil, adminPluginMutationProblem(err)
		}
		return nil, nil
	})
	probe := op("POST", "config/test", "testAdminPluginInstallationConfig", "Probe one prospective configuration by starting a temporary plugin instance and running its connection check; nothing is stored. The check calls the plugin's provider and is bounded by a server timeout; never automatically retry an uncertain result. A failed check is a 200 result with success false.", RetrySafetyNonRetryable)
	Register(reg, probe, func(ctx context.Context, in *AdminPluginConfigInput) (*AdminPluginConnectionCheckOutput, error) {
		if reg.deps.AdminPluginConfiguration == nil {
			return nil, unavailable("plugin configuration")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		if strings.TrimSpace(in.Body.Key) == "" {
			return nil, validationProblem(locationBody+".key", codeInvalid, "A configuration key is required.")
		}
		result, err := reg.deps.AdminPluginConfiguration.TestAdminPluginConfig(ctx, id, handlers.PluginConfigInput{Key: in.Body.Key, Value: map[string]any(in.Body.Value), ClearSecrets: in.Body.ClearSecrets})
		if err != nil {
			return nil, adminPluginMutationProblem(err)
		}
		return &AdminPluginConnectionCheckOutput{Body: AdminPluginConnectionCheck{Success: result.Success, Message: result.Message}}, nil
	})
	auth := op("PUT", "auth-binding", "updateAdminPluginAuthBinding", "Replace the auth provider binding for one capability and mark a server restart required. The whole row is assigned, so repeating the request converges on one stored binding.", RetrySafetyNaturalIdempotent)
	auth.DefaultStatus = http.StatusNoContent
	Register(reg, auth, func(ctx context.Context, in *AdminPluginAuthBindingInput) (*AdminPluginAuthBindingOutput, error) {
		if reg.deps.AdminPluginConfiguration == nil {
			return nil, unavailable("plugin configuration")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		if strings.TrimSpace(in.Body.CapabilityID) == "" {
			return nil, validationProblem(locationBody+".capability_id", codeInvalid, "A capability ID is required.")
		}
		b := in.Body
		if err := reg.deps.AdminPluginConfiguration.SetAdminPluginAuthBinding(ctx, id, handlers.PluginAuthBindingInput{CapabilityID: b.CapabilityID, Enabled: b.Enabled, DisplayOrder: b.DisplayOrder, AutoProvision: b.AutoProvision, DefaultLogin: b.DefaultLogin}); err != nil {
			return nil, adminPluginMutationProblem(err)
		}
		return &AdminPluginAuthBindingOutput{RestartRequired: restartRequiredHeaderValue}, nil
	})
	task := op("PUT", "task-bindings/{capability_id}", "updateAdminPluginTaskBinding", "Replace the scheduled task binding for one capability and mark a server restart required. The whole row is assigned, so repeating the request converges on one stored binding.", RetrySafetyNaturalIdempotent)
	Register(reg, task, func(ctx context.Context, in *AdminPluginTaskBindingInput) (*AdminPluginTaskBindingOutput, error) {
		if reg.deps.AdminPluginConfiguration == nil {
			return nil, unavailable("plugin configuration")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		if strings.TrimSpace(in.CapabilityID) == "" {
			return nil, validationProblem("path.capability_id", codeInvalid, "A capability ID is required.")
		}
		if err := reg.deps.AdminPluginConfiguration.SetAdminPluginTaskBinding(ctx, id, in.CapabilityID, handlers.PluginTaskBindingInput{Enabled: in.Body.Enabled, Trigger: map[string]any(in.Body.Trigger)}); err != nil {
			return nil, adminPluginMutationProblem(err)
		}
		return &AdminPluginTaskBindingOutput{Body: AdminPluginTaskBindingResult{RestartRequired: true}}, nil
	})
}

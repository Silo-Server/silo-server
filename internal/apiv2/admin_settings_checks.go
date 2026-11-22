package apiv2

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminSettingsCheckService interface {
	CheckAdminSettingsConnection(context.Context, string, map[string]string, []string) (handlers.AdminSettingsCheckResult, error)
}
type AdminSettingsCheckInput struct {
	Kind string `path:"kind"`
	Body struct {
		Values    AdminSettingValues `json:"values"`
		DirtyKeys []string           `json:"dirty_keys" maxItems:"256"`
	}
}
type AdminSettingsCheckResult struct {
	Success     bool              `json:"success"`
	Message     string            `json:"message"`
	MessageKey  string            `json:"message_key,omitempty" doc:"Key of the shared translation catalog message whose English is message. A client that has the key shows its translation, filling message_args, and otherwise shows message. Absent when message has no catalog entry" example:"web.admin.common.connectionCheck.result.redisConnected"`
	MessageArgs map[string]string `json:"message_args,omitempty" doc:"Values of the message_key message's placeholders by name; present only with message_key, and absent when the message has none" example:"{\"dimensions\":\"1024\"}"`
}
type AdminSettingsCheckOutput struct{ Body AdminSettingsCheckResult }

func registerAdminSettingsChecks(reg *Registry) {
	op := Operation{Operation: humaOp("POST", Prefix+"/admin/settings/check/{kind}", "checkAdminSettingsConnection", "admin-settings", "Perform one synchronous connection check. Provider calls may bill or write temporary objects; never automatically retry an uncertain result."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, reg.checkAdminSettingsConnection)
}
func (reg *Registry) checkAdminSettingsConnection(ctx context.Context, in *AdminSettingsCheckInput) (*AdminSettingsCheckOutput, error) {
	if reg.deps.AdminSettingsChecks == nil {
		return nil, unavailable("administrator settings checks")
	}
	result, err := reg.deps.AdminSettingsChecks.CheckAdminSettingsConnection(ctx, in.Kind, map[string]string(in.Body.Values), in.Body.DirtyKeys)
	if errors.Is(err, handlers.ErrAdminSettingsCheckKind) || errors.Is(err, handlers.ErrAdminSettingsCheckConfig) {
		return nil, NewProblem(TypeValidationFailed, "Unsupported check kind or invalid configuration")
	}
	if err != nil {
		return nil, adminSettingsInspectionProblem(err)
	}
	return &AdminSettingsCheckOutput{Body: AdminSettingsCheckResult{Success: result.Success, Message: result.Message, MessageKey: result.MessageKey, MessageArgs: result.MessageArgs}}, nil
}

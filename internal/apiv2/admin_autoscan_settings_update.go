package apiv2

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type AdminAutoscanSettingsUpdateService interface {
	UpdateAdminAutoscanSettings(context.Context, autoscan.Settings) (handlers.AdminAutoscanSettingsWriteView, error)
}
type AdminAutoscanSettingsUpdateBody struct {
	Enabled                    bool `json:"enabled"`
	DefaultPollIntervalSeconds int  `json:"default_poll_interval_seconds" minimum:"1" maximum:"2147483647"`
	DebounceSeconds            int  `json:"debounce_seconds" minimum:"0" maximum:"2147483647"`
}
type AdminAutoscanSettingsUpdateInput struct {
	Body AdminAutoscanSettingsUpdateBody
}
type AdminAutoscanSettingsUpdateOutput struct {
	Body AdminAutoscanSettingsUpdateResult
}
type AdminAutoscanSettingsUpdateResult struct {
	Settings        AdminAutoscanSettings `json:"settings"`
	RescheduleState string                `json:"reschedule_state" enum:"applied,failed,not_configured" doc:"Result of this process's optional reschedule call, not a durable or cluster-wide ordering guarantee."`
}

func registerAdminAutoscanSettingsUpdate(reg *Registry) {
	op := Operation{Operation: humaOp("PUT", Prefix+"/admin/autoscan/settings", "updateAdminAutoscanSettings", "admin-autoscan", "Persist desired settings, then attempt optional poll-task rescheduling. Reschedule failure is non-fatal. No automatic retry, revision precondition or durable runtime acknowledgement."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanSettingsUpdateInput) (*AdminAutoscanSettingsUpdateOutput, error) {
		if reg.deps.AdminAutoscanSettingsUpdates == nil {
			return nil, unavailable("autoscan settings")
		}
		b := in.Body
		view, err := reg.deps.AdminAutoscanSettingsUpdates.UpdateAdminAutoscanSettings(ctx, autoscan.Settings{Enabled: b.Enabled, DefaultPollIntervalSeconds: b.DefaultPollIntervalSeconds, DebounceSeconds: b.DebounceSeconds})
		switch {
		case errors.Is(err, handlers.ErrAdminAutoscanSettingsWriteUnavailable):
			return nil, unavailable("autoscan settings")
		case errors.Is(err, handlers.ErrAdminAutoscanSettingsWriteInvalid):
			return nil, NewProblem(TypeValidationFailed, "Invalid autoscan interval or debounce.")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Autoscan settings persistence could not be confirmed.")
		}
		s := view.Settings
		return &AdminAutoscanSettingsUpdateOutput{Body: AdminAutoscanSettingsUpdateResult{Settings: AdminAutoscanSettings{Enabled: s.Enabled, DefaultPollIntervalSeconds: s.DefaultPollIntervalSeconds, DebounceSeconds: s.DebounceSeconds}, RescheduleState: view.RescheduleState}}, nil
	})
}

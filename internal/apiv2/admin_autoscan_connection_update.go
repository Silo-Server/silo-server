package apiv2

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type AdminAutoscanConnectionUpdateService interface {
	UpdateAdminAutoscanConnection(context.Context, string, handlers.AdminAutoscanConnectionUpdateInput) (handlers.AdminAutoscanConnectionView, error)
}
type AdminAutoscanConnectionUpdateInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"256"`
	Body AdminAutoscanConnectionCreateBody
}
type AdminAutoscanConnectionUpdateOutput struct{ Body AdminAutoscanConnection }

func registerAdminAutoscanConnectionUpdate(reg *Registry) {
	op := Operation{Operation: humaOp("PUT", Prefix+"/admin/autoscan/connections/{id}", "updateAdminAutoscanConnection", "admin-autoscan", "Update one stored connection using existing encryption; a blank key preserves the stored key. No probe, replay identity, automatic retry or durable job; reconcile uncertain completion explicitly."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanConnectionUpdateInput) (*AdminAutoscanConnectionUpdateOutput, error) {
		if reg.deps.AdminAutoscanConnectionUpdate == nil {
			return nil, unavailable("autoscan connections")
		}
		b := in.Body
		row, err := reg.deps.AdminAutoscanConnectionUpdate.UpdateAdminAutoscanConnection(ctx, in.ID, handlers.AdminAutoscanConnectionUpdateInput{Name: b.Name, Kind: b.Kind, BaseURL: b.BaseURL, APIKeyRef: b.APIKeyRef, RequestIntegrationID: b.RequestIntegrationID})
		switch {
		case errors.Is(err, handlers.ErrAdminAutoscanConnectionUpdateUnavailable):
			return nil, unavailable("autoscan connections")
		case errors.Is(err, handlers.ErrAdminAutoscanConnectionUpdateInvalid):
			return nil, NewProblem(TypeValidationFailed, "A connection name and base URL or request integration are required.")
		case errors.Is(err, autoscan.ErrNotFound):
			return nil, NewProblem(TypeNotFound, "Autoscan connection not found.")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Connection update could not be confirmed.")
		}
		if row.ID == "" {
			return nil, NewProblem(TypeInternalError, "Connection update returned no identity; completion is uncertain.")
		}
		return &AdminAutoscanConnectionUpdateOutput{Body: adminAutoscanConnectionOf(row)}, nil
	})
}

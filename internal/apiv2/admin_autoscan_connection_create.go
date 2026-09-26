package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminAutoscanConnectionCreationService interface {
	CreateAdminAutoscanConnection(context.Context, handlers.AdminAutoscanConnectionCreateInput) (handlers.AdminAutoscanConnectionView, error)
}
type AdminAutoscanConnectionCreateBody struct {
	Name                 string  `json:"name" minLength:"1" maxLength:"1024"`
	Kind                 string  `json:"kind" maxLength:"256"`
	BaseURL              string  `json:"base_url,omitempty" maxLength:"8192"`
	APIKeyRef            string  `json:"api_key_ref,omitempty" maxLength:"8192" writeOnly:"true"`
	RequestIntegrationID *string `json:"request_integration_id,omitempty" maxLength:"256"`
}
type AdminAutoscanConnectionCreateInput struct {
	Body AdminAutoscanConnectionCreateBody
}
type AdminAutoscanConnectionCreateOutput struct{ Body AdminAutoscanConnection }

func registerAdminAutoscanConnectionCreate(reg *Registry) {
	op := Operation{Operation: humaOp("POST", Prefix+"/admin/autoscan/connections", "createAdminAutoscanConnection", "admin-autoscan", "Create one stored connection using existing encryption. No probe, replay identity, automatic retry or durable job; reconcile uncertain completion explicitly."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	op.DefaultStatus = http.StatusCreated
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanConnectionCreateInput) (*AdminAutoscanConnectionCreateOutput, error) {
		if reg.deps.AdminAutoscanConnectionCreation == nil {
			return nil, unavailable("autoscan connections")
		}
		b := in.Body
		row, err := reg.deps.AdminAutoscanConnectionCreation.CreateAdminAutoscanConnection(ctx, handlers.AdminAutoscanConnectionCreateInput{Name: b.Name, Kind: b.Kind, BaseURL: b.BaseURL, APIKeyRef: b.APIKeyRef, RequestIntegrationID: b.RequestIntegrationID})
		switch {
		case errors.Is(err, handlers.ErrAdminAutoscanConnectionCreateUnavailable):
			return nil, unavailable("autoscan connections")
		case errors.Is(err, handlers.ErrAdminAutoscanConnectionCreateInvalid):
			return nil, NewProblem(TypeValidationFailed, "A connection name and base URL or request integration are required.")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Connection creation could not be confirmed.")
		}
		if row.ID == "" {
			return nil, NewProblem(TypeInternalError, "Connection creation returned no identity; completion is uncertain.")
		}
		return &AdminAutoscanConnectionCreateOutput{Body: adminAutoscanConnectionOf(row)}, nil
	})
}

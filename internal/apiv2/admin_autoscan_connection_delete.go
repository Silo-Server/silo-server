package apiv2

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type AdminAutoscanConnectionDeleteService interface {
	DeleteAdminAutoscanConnection(context.Context, string) error
}
type AdminAutoscanConnectionDeleteInput struct {
	ID string `path:"id" minLength:"1" maxLength:"256"`
}

func registerAdminAutoscanConnectionDelete(reg *Registry) {
	op := Operation{Operation: humaOp("DELETE", Prefix+"/admin/autoscan/connections/{id}", "deleteAdminAutoscanConnection", "admin-autoscan", "Delete one stored connection. Referencing sources prevent deletion. No replay identity or durable receipt; reconcile uncertain completion before explicit resubmission."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanConnectionDeleteInput) (*struct{}, error) {
		if reg.deps.AdminAutoscanConnectionDeletes == nil {
			return nil, unavailable("autoscan connections")
		}
		err := reg.deps.AdminAutoscanConnectionDeletes.DeleteAdminAutoscanConnection(ctx, in.ID)
		switch {
		case errors.Is(err, handlers.ErrAdminAutoscanConnectionDeleteUnavailable):
			return nil, unavailable("autoscan connections")
		case errors.Is(err, autoscan.ErrNotFound):
			return nil, NewProblem(TypeNotFound, "Autoscan connection not found.")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Connection deletion could not be confirmed. Refresh connections and check source bindings before submitting again.")
		}
		return nil, nil
	})
}

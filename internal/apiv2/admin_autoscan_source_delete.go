package apiv2

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type AdminAutoscanSourceDeleteService interface {
	DeleteAdminAutoscanSource(context.Context, string) error
}
type AdminAutoscanSourceDeleteInput struct {
	ID string `path:"id" minLength:"1" maxLength:"256"`
}

func registerAdminAutoscanSourceDelete(reg *Registry) {
	op := Operation{Operation: humaOp("DELETE", Prefix+"/admin/autoscan/sources/{id}", "deleteAdminAutoscanSource", "admin-autoscan", "Delete one stored source. Webhook endpoint and delivery rows cascade; event history is retained without its source reference. Already-running work is not canceled. No replay identity or durable receipt; reconcile uncertain completion before explicit resubmission."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanSourceDeleteInput) (*struct{}, error) {
		if reg.deps.AdminAutoscanSourceDeletes == nil {
			return nil, unavailable("autoscan sources")
		}
		err := reg.deps.AdminAutoscanSourceDeletes.DeleteAdminAutoscanSource(ctx, in.ID)
		switch {
		case errors.Is(err, handlers.ErrAdminAutoscanSourceDeleteUnavailable):
			return nil, unavailable("autoscan sources")
		case errors.Is(err, autoscan.ErrNotFound):
			return nil, NewProblem(TypeNotFound, "Autoscan source not found.")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Source deletion could not be confirmed. Refresh sources before submitting again; running work may continue.")
		}
		return nil, nil
	})
}

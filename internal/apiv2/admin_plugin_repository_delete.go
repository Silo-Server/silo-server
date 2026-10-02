package apiv2

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type AdminPluginRepositoryDeleteService interface {
	Delete(context.Context, int) error
}
type AdminPluginRepositoryDeleteInput struct {
	ID string `path:"id" pattern:"^[1-9][0-9]*$" maxLength:"19"`
}

func registerAdminPluginRepositoryDelete(reg *Registry) {
	op := Operation{Operation: humaOp("DELETE", Prefix+"/admin/plugins/repositories/{id}", "deleteAdminPluginRepository", "admin-plugins", "Delete one stored external repository. Managed repositories cannot be deleted. No installation removal, runtime operation or replay identity; reconcile uncertain completion explicitly."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *AdminPluginRepositoryDeleteInput) (*struct{}, error) {
		id, err := intOfID(ID(in.ID))
		if err != nil || id <= 0 {
			return nil, NewProblem(TypeValidationFailed, "A positive repository ID is required.")
		}
		if reg.deps.AdminPluginRepositoryDeletes == nil {
			return nil, unavailable("plugin repositories")
		}
		err = reg.deps.AdminPluginRepositoryDeletes.Delete(ctx, id)
		switch {
		case errors.Is(err, plugins.ErrRepositoryNotFound):
			return nil, NewProblem(TypeNotFound, "Plugin repository not found.")
		case errors.Is(err, plugins.ErrManagedRepositoryReadOnly):
			return nil, NewProblem(TypeConflict, "Managed plugin repositories cannot be deleted.")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Repository deletion could not be confirmed.")
		}
		return nil, nil
	})
}

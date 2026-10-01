package apiv2

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type AdminPluginRepositoryUpdateService interface {
	Update(context.Context, int, plugins.UpdateRepositoryInput) error
	GetByID(context.Context, int) (*plugins.Repository, error)
}
type AdminPluginRepositoryUpdateBody struct {
	URL         string `json:"url,omitempty" maxLength:"8192"`
	DisplayName string `json:"display_name,omitempty" maxLength:"1024"`
	Enabled     *bool  `json:"enabled,omitempty"`
}
type AdminPluginRepositoryUpdateInput struct {
	ID   string `path:"id" pattern:"^[1-9][0-9]*$" maxLength:"19"`
	Body AdminPluginRepositoryUpdateBody
}
type AdminPluginRepositoryUpdateOutput struct{ Body AdminPluginRepository }

func registerAdminPluginRepositoryUpdate(reg *Registry) {
	op := Operation{Operation: humaOp("PUT", Prefix+"/admin/plugins/repositories/{id}", "updateAdminPluginRepository", "admin-plugins", "Update stored repository configuration. Blank name/URL are ignored. Managed configuration is read-only. Readback is current state, not a write revision; reconcile uncertain completion without replay."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *AdminPluginRepositoryUpdateInput) (*AdminPluginRepositoryUpdateOutput, error) {
		id, err := intOfID(ID(in.ID))
		if err != nil || id <= 0 {
			return nil, NewProblem(TypeValidationFailed, "A positive repository ID is required.")
		}
		if reg.deps.AdminPluginRepositoryUpdates == nil {
			return nil, unavailable("plugin repositories")
		}
		b := in.Body
		input := plugins.UpdateRepositoryInput{Enabled: b.Enabled}
		if strings.TrimSpace(b.URL) != "" {
			input.URL = new(b.URL)
		}
		if strings.TrimSpace(b.DisplayName) != "" {
			input.DisplayName = new(b.DisplayName)
		}
		err = reg.deps.AdminPluginRepositoryUpdates.Update(ctx, id, input)
		switch {
		case errors.Is(err, plugins.ErrRepositoryNotFound):
			return nil, NewProblem(TypeNotFound, "Plugin repository not found.")
		case errors.Is(err, plugins.ErrManagedRepositoryReadOnly):
			return nil, NewProblem(TypeConflict, "Managed plugin repositories are controlled by catalog settings.")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Repository update could not be confirmed.")
		}
		row, err := reg.deps.AdminPluginRepositoryUpdates.GetByID(ctx, id)
		if err != nil || row == nil || row.ID != id {
			return nil, NewProblem(TypeInternalError, "Repository update could not be read back; completion is uncertain.")
		}
		return &AdminPluginRepositoryUpdateOutput{Body: adminPluginRepositoryOf(row)}, nil
	})
}

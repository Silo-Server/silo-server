package apiv2

import (
	"context"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type AdminPluginRepositoryCreationService interface {
	Create(context.Context, plugins.CreateRepositoryInput) (*plugins.Repository, error)
}
type AdminPluginRepositoryCreateBody struct {
	URL         string `json:"url" minLength:"1" maxLength:"8192"`
	DisplayName string `json:"display_name" minLength:"1" maxLength:"1024"`
	Enabled     *bool  `json:"enabled,omitempty"`
}
type AdminPluginRepositoryCreateInput struct {
	Body AdminPluginRepositoryCreateBody
}
type AdminPluginRepositoryCreateOutput struct{ Body AdminPluginRepository }

func registerAdminPluginRepositoryCreate(reg *Registry) {
	op := Operation{Operation: humaOp("POST", Prefix+"/admin/plugins/repositories", "createAdminPluginRepository", "admin-plugins", "Create one stored repository configuration. No remote fetch, installation, replay identity or automatic retry; uncertain completion must be reconciled explicitly."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	op.DefaultStatus = http.StatusCreated
	Register(reg, op, func(ctx context.Context, in *AdminPluginRepositoryCreateInput) (*AdminPluginRepositoryCreateOutput, error) {
		if reg.deps.AdminPluginRepositoryCreation == nil {
			return nil, unavailable("plugin repositories")
		}
		b := in.Body
		if strings.TrimSpace(b.URL) == "" || strings.TrimSpace(b.DisplayName) == "" {
			return nil, NewProblem(TypeValidationFailed, "Repository URL and display name are required.")
		}
		if b.URL == plugins.DefaultRepositoryURL || b.URL == plugins.ApprovedCommunityRepositoryURL {
			return nil, NewProblem(TypeValidationFailed, "Use catalog settings to manage built-in plugin repositories.")
		}
		row, err := reg.deps.AdminPluginRepositoryCreation.Create(ctx, plugins.CreateRepositoryInput{URL: b.URL, DisplayName: b.DisplayName, Enabled: b.Enabled})
		if err != nil {
			return nil, serviceProblem(err)
		}
		if row == nil || row.ID <= 0 {
			return nil, NewProblem(TypeInternalError, "Repository creation returned no valid record; completion is uncertain.")
		}
		return &AdminPluginRepositoryCreateOutput{Body: adminPluginRepositoryOf(row)}, nil
	})
}

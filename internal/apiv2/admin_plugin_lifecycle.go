package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// AdminPluginLifecycleService is the slice of *handlers.PluginHandler the
// installation lifecycle uses: install from catalog or archive URL, partial
// assignment, apply the recorded update, restart the process, and uninstall.
// Every method refuses the reserved builtin row and reports an unknown
// installation as plugins.ErrInstallationNotFound.
type AdminPluginLifecycleService interface {
	CreateAdminPluginInstallation(context.Context, handlers.PluginInstallationCreateInput) (handlers.PluginInstallationView, error)
	UpdateAdminPluginInstallation(context.Context, int, handlers.PluginInstallationUpdateInput) (handlers.PluginInstallationView, error)
	ApplyAdminPluginUpdate(context.Context, int) (handlers.PluginInstallationView, error)
	RestartAdminPluginInstallation(context.Context, int) (handlers.PluginInstallationView, error)
	DeleteAdminPluginInstallation(context.Context, int) error
}

// AdminPluginInstallCreate names either a catalog target or an archive URL.
type AdminPluginInstallCreate struct {
	RepositoryID *ID    `json:"repository_id,omitempty" pattern:"^[1-9][0-9]*$" doc:"Catalog repository; required with plugin_id and version"`
	PluginID     string `json:"plugin_id,omitempty" maxLength:"256"`
	Version      string `json:"version,omitempty" maxLength:"128"`
	ArchiveURL   string `json:"archive_url,omitempty" maxLength:"2048" format:"uri" doc:"Direct plugin archive; cannot be combined with catalog fields"`
}
type AdminPluginInstallCreateInput struct {
	RawBody []byte
	Body    AdminPluginInstallCreate
}

// AdminPluginInstallationUpdate is the partial assignment; omitted fields keep
// their stored values.
type AdminPluginInstallationUpdate struct {
	Enabled      *bool   `json:"enabled,omitempty"`
	UpdatePolicy *string `json:"update_policy,omitempty" enum:"auto,notify,off,manual"`
}
type AdminPluginInstallationUpdateInput struct {
	ID      ID `path:"id" pattern:"^[1-9][0-9]*$"`
	RawBody []byte
	Body    AdminPluginInstallationUpdate
}
type AdminPluginInstallationIDInput struct {
	ID ID `path:"id" pattern:"^[1-9][0-9]*$"`
}
type AdminPluginInstallationOutput struct{ Body AdminPluginInstallation }

func adminPluginLifecycleProblem(err error) error {
	var apiErr *handlers.APIError
	switch {
	case errors.Is(err, handlers.ErrPluginUpdateUnavailable):
		return NewProblem(TypeConflict, "This installation has no applicable update.")
	case errors.Is(err, plugins.ErrInstallationDisabled):
		return NewProblem(TypeConflict, "This installation is disabled; enable it before restarting.")
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest && apiErr.Field != "":
		return validationProblem(locationBody+"."+apiErr.Field, codeInvalid, apiErr.Message)
	}
	return adminPluginMutationProblem(err)
}

func adminPluginInstallationOutput(view handlers.PluginInstallationView, err error) (*AdminPluginInstallationOutput, error) {
	if err != nil {
		return nil, adminPluginLifecycleProblem(err)
	}
	out, err := adminPluginInstallationOf(view)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &AdminPluginInstallationOutput{Body: out}, nil
}

func registerAdminPluginLifecycle(reg *Registry) {
	op := func(method, path, id, summary string, safety RetrySafety) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/plugins/installations"+path, id, "admin-plugins", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, RetrySafety: safety}
		o.MaxBodyBytes = 64 << 10
		return o
	}
	create := op(http.MethodPost, "", "createAdminPluginInstallation", "Install a plugin from a catalog repository (repository_id, plugin_id, version) or a direct archive URL, fetching over the network. An installation with the same plugin_id is stopped and replaced rather than duplicated. There is no replay identity: a lost response may follow a committed install; reconcile from the installation list and never automatically retry.", RetrySafetyNonRetryable)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, func(ctx context.Context, in *AdminPluginInstallCreateInput) (*AdminPluginInstallationOutput, error) {
		if reg.deps.AdminPluginLifecycle == nil {
			return nil, unavailable("plugin lifecycle")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		input := handlers.PluginInstallationCreateInput{PluginID: in.Body.PluginID, Version: in.Body.Version, ArchiveURL: in.Body.ArchiveURL}
		if in.Body.RepositoryID != nil {
			n, p := adminPluginInstallationID(*in.Body.RepositoryID)
			if p != nil {
				return nil, validationProblem(locationBody+".repository_id", codeInvalid, "Invalid plugin repository ID.")
			}
			input.RepositoryID = &n
		}
		hasCatalog := input.RepositoryID != nil || strings.TrimSpace(input.PluginID) != "" || strings.TrimSpace(input.Version) != ""
		if !hasCatalog && strings.TrimSpace(input.ArchiveURL) == "" {
			return nil, validationProblem(locationBody+".archive_url", codeRequired, "Supply archive_url or repository_id, plugin_id and version.")
		}
		return adminPluginInstallationOutput(reg.deps.AdminPluginLifecycle.CreateAdminPluginInstallation(ctx, input))
	})
	update := op(http.MethodPut, "/{id}", "updateAdminPluginInstallation", "Assign enabled and/or update_policy on one installation. Disabling stops the running plugin first. Repeating the same assignment converges on one stored row.", RetrySafetyNaturalIdempotent)
	update.Errors = append(update.Errors, http.StatusNotFound, http.StatusConflict)
	Register(reg, update, func(ctx context.Context, in *AdminPluginInstallationUpdateInput) (*AdminPluginInstallationOutput, error) {
		if reg.deps.AdminPluginLifecycle == nil {
			return nil, unavailable("plugin lifecycle")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		if in.Body.Enabled == nil && in.Body.UpdatePolicy == nil {
			return nil, validationProblem(locationBody, codeRequired, "Supply enabled or update_policy.")
		}
		return adminPluginInstallationOutput(reg.deps.AdminPluginLifecycle.UpdateAdminPluginInstallation(ctx, id, handlers.PluginInstallationUpdateInput{Enabled: in.Body.Enabled, UpdatePolicy: in.Body.UpdatePolicy}))
	})
	apply := op(http.MethodPost, "/{id}/update", "applyAdminPluginUpdate", "Install the recorded available version from the installation's repository, fetching over the network, and clear the marker. No applicable update is 409. There is no replay identity: a lost response may follow a committed update; reconcile from the installation and never automatically retry.", RetrySafetyNonRetryable)
	apply.Errors = append(apply.Errors, http.StatusNotFound, http.StatusConflict)
	Register(reg, apply, func(ctx context.Context, in *AdminPluginInstallationIDInput) (*AdminPluginInstallationOutput, error) {
		if reg.deps.AdminPluginLifecycle == nil {
			return nil, unavailable("plugin lifecycle")
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		return adminPluginInstallationOutput(reg.deps.AdminPluginLifecycle.ApplyAdminPluginUpdate(ctx, id))
	})
	restart := op(http.MethodPost, "/{id}/restart", "restartAdminPluginInstallation", "Stop the installation's process and, for a resident plugin (one the server supervises, such as a network access provider), start it again with a fresh failure budget; the response's runtime reports the outcome, including a launch that failed. A non-resident plugin is only stopped and launches on its next use. A disabled installation is 409. Repeating the request converges on one running process.", RetrySafetyNaturalIdempotent)
	restart.Errors = append(restart.Errors, http.StatusNotFound, http.StatusConflict)
	Register(reg, restart, func(ctx context.Context, in *AdminPluginInstallationIDInput) (*AdminPluginInstallationOutput, error) {
		if reg.deps.AdminPluginLifecycle == nil {
			return nil, unavailable("plugin lifecycle")
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		return adminPluginInstallationOutput(reg.deps.AdminPluginLifecycle.RestartAdminPluginInstallation(ctx, id))
	})
	remove := op(http.MethodDelete, "/{id}", "deleteAdminPluginInstallation", "Stop the plugin, delete the installation row (configuration, bindings and archives cascade) and remove its files. Row delete and file removal are not one transaction and a repeat finds no row: a later 404 is not this caller's receipt; never automatically retry.", RetrySafetyNonRetryable)
	remove.DefaultStatus = http.StatusNoContent
	remove.Errors = append(remove.Errors, http.StatusNotFound, http.StatusConflict)
	Register(reg, remove, func(ctx context.Context, in *AdminPluginInstallationIDInput) (*struct{}, error) {
		if reg.deps.AdminPluginLifecycle == nil {
			return nil, unavailable("plugin lifecycle")
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		if err := reg.deps.AdminPluginLifecycle.DeleteAdminPluginInstallation(ctx, id); err != nil {
			return nil, adminPluginLifecycleProblem(err)
		}
		return nil, nil
	})
}

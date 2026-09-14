package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// PluginInstallationCreateInput is one install request: either a catalog
// target (repository_id, plugin_id, version) or a direct archive URL.
type PluginInstallationCreateInput struct {
	RepositoryID *int
	PluginID     string
	Version      string
	ArchiveURL   string
}

// PluginInstallationUpdateInput is the partial installation assignment.
type PluginInstallationUpdateInput struct {
	Enabled      *bool
	UpdatePolicy *string
}

// ErrPluginUpdateUnavailable reports an apply-update on an installation with
// no recorded available version or no repository to update from.
var ErrPluginUpdateUnavailable = errors.New("plugin has no applicable update")

func (h *PluginHandler) pluginLifecycleReady() error {
	if h == nil || h.installations == nil || h.service == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Plugin service not configured")
	}
	return nil
}

// CreateAdminPluginInstallation installs from the catalog or a remote archive
// and returns the resulting installation. Both paths fetch over the network
// through the plugin service's injectable clients; an existing installation
// of the same plugin_id is stopped and replaced rather than duplicated. There
// is no replay identity: a lost response may follow a committed install.
// Errors: *APIError with Field for request shape refusals; service errors
// otherwise.
func (h *PluginHandler) CreateAdminPluginInstallation(ctx context.Context, in PluginInstallationCreateInput) (PluginInstallationView, error) {
	if err := h.pluginLifecycleReady(); err != nil {
		return PluginInstallationView{}, err
	}
	pluginID, version, archiveURL := strings.TrimSpace(in.PluginID), strings.TrimSpace(in.Version), strings.TrimSpace(in.ArchiveURL)
	hasRepositoryFields := in.RepositoryID != nil || pluginID != "" || version != ""
	var (
		result *plugins.InstallResult
		err    error
	)
	switch {
	case hasRepositoryFields:
		if in.RepositoryID == nil || pluginID == "" || version == "" {
			return PluginInstallationView{}, fieldError("plugin_id", "repository_id, plugin_id, and version are required")
		}
		if archiveURL != "" {
			return PluginInstallationView{}, fieldError("archive_url", "archive_url cannot be combined with repository install fields")
		}
		result, err = h.service.InstallCatalog(ctx, plugins.InstallCatalogRequest{RepositoryID: *in.RepositoryID, PluginID: pluginID, Version: version})
	default:
		if archiveURL == "" {
			return PluginInstallationView{}, fieldError("archive_url", "archive_url is required")
		}
		result, err = h.service.InstallRemote(ctx, plugins.InstallArchiveRequest{ArchiveURL: archiveURL, RepositoryID: in.RepositoryID})
	}
	if err != nil {
		return PluginInstallationView{}, err
	}
	return h.installedPluginView(ctx, result)
}

// installedPluginView appends the installation's metadata providers to the
// library chains and projects the installation.
func (h *PluginHandler) installedPluginView(ctx context.Context, result *plugins.InstallResult) (PluginInstallationView, error) {
	h.syncMetadataProviders(ctx, result.Installation)
	return h.buildInstallationResponse(ctx, result.Installation, result.Manifest)
}

// UpdateAdminPluginInstallation assigns enabled and/or update_policy. Disabling
// stops the running plugin first; any enabled change rebuilds the event
// subscriber index. Repeating the same assignment converges on one row.
// Errors: plugins.ErrInstallationNotFound, ErrPluginBuiltinInstallation.
func (h *PluginHandler) UpdateAdminPluginInstallation(ctx context.Context, id int, in PluginInstallationUpdateInput) (PluginInstallationView, error) {
	if err := h.pluginLifecycleReady(); err != nil {
		return PluginInstallationView{}, err
	}
	current, err := h.installations.GetByID(ctx, id)
	if err != nil {
		return PluginInstallationView{}, err
	}
	if current.IsBuiltin() {
		return PluginInstallationView{}, ErrPluginBuiltinInstallation
	}
	if in.Enabled != nil && !*in.Enabled && current.Enabled {
		if err := h.service.Stop(id); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
			return PluginInstallationView{}, err
		}
	}
	if err := h.installations.Update(ctx, id, plugins.UpdateInstallationInput{Enabled: in.Enabled, UpdatePolicy: in.UpdatePolicy}); err != nil {
		if errors.Is(err, plugins.ErrBuiltinInstallationImmutable) {
			return PluginInstallationView{}, ErrPluginBuiltinInstallation
		}
		return PluginInstallationView{}, err
	}
	if in.Enabled != nil {
		h.service.OnLifecycleChange(ctx)
	}
	installation, err := h.installations.GetByID(ctx, id)
	if err != nil {
		return PluginInstallationView{}, err
	}
	return h.buildInstallationResponse(ctx, installation, nil)
}

// ApplyAdminPluginUpdate installs the recorded available version from the
// installation's repository (a network fetch) and clears the marker. A repeat
// after success finds no available version. Errors:
// plugins.ErrInstallationNotFound, ErrPluginBuiltinInstallation,
// ErrPluginUpdateUnavailable.
func (h *PluginHandler) ApplyAdminPluginUpdate(ctx context.Context, id int) (PluginInstallationView, error) {
	if err := h.pluginLifecycleReady(); err != nil {
		return PluginInstallationView{}, err
	}
	current, err := h.installations.GetByID(ctx, id)
	if err != nil {
		return PluginInstallationView{}, err
	}
	if current.IsBuiltin() {
		return PluginInstallationView{}, ErrPluginBuiltinInstallation
	}
	if current.AvailableVersion == nil || *current.AvailableVersion == "" || current.RepositoryID == nil || *current.RepositoryID == 0 {
		return PluginInstallationView{}, ErrPluginUpdateUnavailable
	}
	installation, err := h.service.UpdateToAvailableVersion(ctx, id)
	if err != nil {
		return PluginInstallationView{}, err
	}
	h.syncMetadataProviders(ctx, installation)
	return h.buildInstallationResponse(ctx, installation, nil)
}

// RestartAdminPluginInstallation stops the installation's process and, for a
// resident plugin, starts it again with a fresh failure budget, so an
// administrator can bring back a resident the supervisor parked as failed. A
// non-resident plugin is only stopped; its next call launches it. A launch
// failure is reported through the returned view's runtime state rather than
// as an error. Errors: plugins.ErrInstallationNotFound,
// plugins.ErrInstallationDisabled, ErrPluginBuiltinInstallation.
func (h *PluginHandler) RestartAdminPluginInstallation(ctx context.Context, id int) (PluginInstallationView, error) {
	if err := h.pluginLifecycleReady(); err != nil {
		return PluginInstallationView{}, err
	}
	current, err := h.installations.GetByID(ctx, id)
	if err != nil {
		return PluginInstallationView{}, err
	}
	if current.IsBuiltin() {
		return PluginInstallationView{}, ErrPluginBuiltinInstallation
	}
	if err := h.service.RestartInstallation(ctx, id); err != nil {
		return PluginInstallationView{}, err
	}
	return h.buildInstallationResponse(ctx, current, nil)
}

// DeleteAdminPluginInstallation stops the plugin, deletes its row (dependent
// rows cascade) and removes its files. The row delete and the file removal are
// not one transaction, and a repeat finds no row: a lost response is not a
// receipt. Errors: plugins.ErrInstallationNotFound, ErrPluginBuiltinInstallation.
func (h *PluginHandler) DeleteAdminPluginInstallation(ctx context.Context, id int) error {
	if h == nil || h.installations == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Plugin stores not configured")
	}
	installation, err := h.installations.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if installation.IsBuiltin() {
		return ErrPluginBuiltinInstallation
	}
	stopped := false
	if h.service != nil {
		if err := h.service.Stop(id); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
			return err
		}
		stopped = true
	}
	if err := h.installations.Delete(ctx, id); err != nil {
		if stopped && installation.Enabled {
			if _, restartErr := h.service.Start(ctx, id); restartErr != nil {
				slog.ErrorContext(ctx, "restarting plugin after failed uninstall", "component", "api", "installation_id", id, "error", restartErr)
			}
		}
		if errors.Is(err, plugins.ErrBuiltinInstallationImmutable) {
			return ErrPluginBuiltinInstallation
		}
		return err
	}
	if h.service != nil {
		h.service.OnLifecycleChange(ctx)
	}
	return nil
}

// writePluginLifecycleError keeps the frozen bridge answers for the seams.
func writePluginLifecycleError(w http.ResponseWriter, r *http.Request, err error, action string) {
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
	case errors.Is(err, plugins.ErrInstallationNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
	case errors.Is(err, ErrPluginBuiltinInstallation):
		if action == "uninstall" {
			writeError(w, http.StatusConflict, "builtin_installation", "Built-in host providers cannot be uninstalled")
			return
		}
		writeError(w, http.StatusConflict, "builtin_installation", "Built-in host providers cannot be modified")
	default:
		slog.ErrorContext(r.Context(), "plugin installation lifecycle failed", "component", "api", "action", action, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to "+action+" plugin")
	}
}

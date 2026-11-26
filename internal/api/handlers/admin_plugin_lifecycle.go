package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

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

// lifecycleCommitTimeout bounds the work a lifecycle change does after its
// write committed.
const lifecycleCommitTimeout = 30 * time.Second

// stopAfterCommit stops installation id's plugin once a change that disables
// or removes it has committed, through the service or stopForTest. The
// service keeps it running only when it reads the row and finds it enabled,
// so an enable that committed since keeps its process; a row that is gone or
// cannot be read is stopped (Service.StopUnlessEnabled). The read is detached
// from the request: the change already holds. A failure, including a failed
// read, is logged: the row already says the plugin is off, and reconciliation
// on every node stops a process whose row no longer runs.
func (h *PluginHandler) stopAfterCommit(ctx context.Context, id int) {
	stop := h.stopForTest
	if stop == nil {
		stop = func(id int) error {
			detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleCommitTimeout)
			defer cancel()
			return h.service.StopUnlessEnabled(detached, id)
		}
	}
	if err := stop(id); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
		slog.WarnContext(ctx, "stopping plugin after its change was saved", "component", "api", "installation_id", id, "error", err)
	}
}

// afterLifecycleCommit reconciles and notifies after a committed lifecycle
// change. The change already holds, so the request's cancellation does not
// cut this short; a timeout bounds it.
func (h *PluginHandler) afterLifecycleCommit(ctx context.Context) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleCommitTimeout)
	defer cancel()
	h.service.OnLifecycleChange(detached)
}

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

// UpdateAdminPluginInstallation assigns enabled and/or update_policy.
// Disabling stops the running plugin once the change is saved, so a refused
// change (the last sign-in provider while password sign-in is off) never
// stopped it; any enabled change rebuilds the event subscriber index.
// Repeating the same assignment converges on one row.
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
	if in.UpdatePolicy != nil && *in.UpdatePolicy != current.UpdatePolicy {
		ownership, err := h.installations.GetProvisionOwnership(ctx, id)
		if err != nil {
			return PluginInstallationView{}, err
		}
		if ownership.VersionPin != "" {
			return PluginInstallationView{}, &plugins.ManagedByEnvironmentError{Field: "update_policy"}
		}
	}

	if err := h.installations.Update(ctx, id, plugins.UpdateInstallationInput{Enabled: in.Enabled, UpdatePolicy: in.UpdatePolicy}); err != nil {
		if errors.Is(err, plugins.ErrBuiltinInstallationImmutable) {
			return PluginInstallationView{}, ErrPluginBuiltinInstallation
		}
		return PluginInstallationView{}, err
	}
	if in.Enabled != nil && !*in.Enabled && current.Enabled {
		h.stopAfterCommit(ctx, id)
	}
	if in.Enabled != nil {
		h.afterLifecycleCommit(ctx)
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
	ownership, err := h.installations.GetProvisionOwnership(ctx, id)
	if err != nil {
		return PluginInstallationView{}, err
	}
	if ownership.VersionPin != "" {
		return PluginInstallationView{}, &plugins.ManagedByEnvironmentError{Field: "version"}
	}

	if current.AvailableVersion == nil || *current.AvailableVersion == "" || current.RepositoryID == nil || *current.RepositoryID == 0 {
		return PluginInstallationView{}, ErrPluginUpdateUnavailable
	}
	installation, err := h.service.UpdateToAvailableVersion(ctx, id)
	if errors.Is(err, plugins.ErrNoUpdateAvailable) {
		// Another install cleared the offer, or made it stale, after the read
		// above: a conflict, not a server error (#4518).
		return PluginInstallationView{}, ErrPluginUpdateUnavailable
	}
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
	// The restart advanced runtime_generation and updated_at on the row; the
	// receipt reflects the row as it is now, not the pre-restart snapshot.
	restarted, err := h.installations.GetByID(ctx, id)
	if err != nil {
		return PluginInstallationView{}, err
	}
	return h.buildInstallationResponse(ctx, restarted, nil)
}

// DeleteAdminPluginInstallation deletes the plugin's row (dependent rows
// cascade), then stops the plugin and removes its files. A refused delete
// (the last sign-in provider while password sign-in is off) never stopped
// it. The row delete and the file removal are not one transaction, and a
// repeat finds no row: a lost response is not a receipt. Errors: plugins.ErrInstallationNotFound, ErrPluginBuiltinInstallation.
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
	ownership, err := h.installations.GetProvisionOwnership(ctx, id)
	if err != nil {
		return err
	}
	if ownership.Managed {
		return &plugins.ManagedByEnvironmentError{Field: "installation"}
	}
	// The plugin stops after the delete commits and before its files go.
	err = h.installations.DeleteThen(ctx, id, func() {
		if h.service != nil || h.stopForTest != nil {
			h.stopAfterCommit(ctx, id)
		}
	})
	if err != nil {
		if errors.Is(err, plugins.ErrBuiltinInstallationImmutable) {
			return ErrPluginBuiltinInstallation
		}
		return err
	}
	if h.service != nil {
		h.afterLifecycleCommit(ctx)
	}
	return nil
}

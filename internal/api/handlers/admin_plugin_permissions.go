package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

func (h *PluginHandler) pluginPermissions() (*plugins.PermissionService, error) {
	if h != nil {
		if permissions := h.service.Permissions(); permissions != nil {
			return permissions, nil
		}
	}
	return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Plugin permissions not configured")
}

func pluginPermissionsError(err error) error {
	if errors.Is(err, plugins.ErrBuiltinInstallationImmutable) {
		return ErrPluginBuiltinInstallation
	}
	return err
}

// GetAdminPluginPermissions returns what the installation asks for and what
// an administrator approved. Errors: plugins.ErrInstallationNotFound,
// ErrPluginBuiltinInstallation.
func (h *PluginHandler) GetAdminPluginPermissions(ctx context.Context, id int) (*plugins.PluginPermissions, error) {
	permissions, err := h.pluginPermissions()
	if err != nil {
		return nil, err
	}
	out, err := permissions.Permissions(ctx, id)
	return out, pluginPermissionsError(err)
}

// ApproveAdminPluginPermissions approves everything the installation asks for
// now as the request's account, issuing or revoking its host API key. check
// runs inside the approval's transaction (plugins.PermissionService.Approve)
// and its error comes back unchanged. Errors: plugins.ErrInstallationNotFound,
// ErrPluginBuiltinInstallation.
func (h *PluginHandler) ApproveAdminPluginPermissions(ctx context.Context, id int, check plugins.ApprovalCheck) (*plugins.PluginPermissions, error) {
	permissions, err := h.pluginPermissions()
	if err != nil {
		return nil, err
	}
	out, err := permissions.Approve(ctx, actorUserID(ctx), id, check)
	return out, ownerError(pluginPermissionsError(err))
}

// AdminPluginPermissionsPending reports which installations ask for
// permissions an administrator hasn't approved.
func (h *PluginHandler) AdminPluginPermissionsPending(ctx context.Context) (map[int]bool, error) {
	permissions, err := h.pluginPermissions()
	if err != nil {
		return nil, err
	}
	return permissions.PendingByInstallation(ctx)
}

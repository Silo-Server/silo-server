package handlers

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// RestorePlugin restores one plugin from a backup in the caller's
// transaction (plugins.Service.RestoreInstallation). A new installation's
// follow-ups run through PluginRestored, which a restore records durably.
func (h *PluginHandler) RestorePlugin(ctx context.Context, tx pgx.Tx, in plugins.RestoreInstallationInput) (plugins.RestoreInstallationResult, func(context.Context), error) {
	if h == nil || h.service == nil {
		return plugins.RestoreInstallationResult{}, nil, plugins.ErrRestoreUnavailable
	}
	return h.service.RestoreInstallation(ctx, tx, in)
}

// PluginRestored runs a restored installation's follow-ups: the lifecycle
// change (EventPluginsChanged, resident reconcile) and the metadata provider
// chain sync an admin upload performs. Both are idempotent, and a sync that
// failed is returned, so the restore keeps its follow-up and runs them again.
func (h *PluginHandler) PluginRestored(ctx context.Context, installationID int) error {
	if h == nil || h.service == nil || h.installations == nil {
		return plugins.ErrRestoreUnavailable
	}
	installation, err := h.installations.GetByID(ctx, installationID)
	if err != nil {
		return err
	}
	h.service.OnLifecycleChange(ctx)
	return h.addMetadataProvidersToChains(ctx, installation)
}

// DecodeStoredConfig decrypts a stored plugin configuration value, for a
// restore preview.
func (h *PluginHandler) DecodeStoredConfig(installationID int, key string, stored []byte) (map[string]any, error) {
	if h == nil || h.service == nil {
		return nil, plugins.ErrRestoreUnavailable
	}
	return h.service.DecodeStoredConfig(installationID, key, stored)
}

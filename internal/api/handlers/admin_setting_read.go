package handlers

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/config"
	"net/http"
)

// ReadAdminSetting preserves protected-key hiding and empty-value absence.
func (h *AdminHandler) ReadAdminSetting(ctx context.Context, key string) (AdminSettingValue, error) {
	if h.SettingsRepo == nil {
		return adminSettingResponse{}, apiError(http.StatusInternalServerError, "internal_error", "Settings store not configured")
	}

	if key == "" {
		return adminSettingResponse{}, apiError(http.StatusBadRequest, "bad_request", "Setting key is required")
	}

	if sensitiveSettingKeys[key] || machineManagedSettingKeys[key] {
		return adminSettingResponse{}, apiError(http.StatusNotFound, "not_found", "Setting not found")
	}

	if value, ok := h.BootstrapSensitiveValues[key]; ok && value != "" {
		return adminSettingResponse{
			Key:             key,
			Value:           value,
			RestartRequired: config.RestartRequired(key),
		}, nil
	}

	value, err := h.SettingsRepo.Get(ctx, key)
	if err != nil {
		return adminSettingResponse{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load setting")
	}
	if value == "" {
		return adminSettingResponse{}, apiError(http.StatusNotFound, "not_found", "Setting not found")
	}

	return adminSettingResponse{
		Key:             key,
		Value:           value,
		RestartRequired: config.RestartRequired(key),
	}, nil
}

package handlers

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/config"
)

var ErrAdminSettingsCheckKind = errors.New("unsupported settings check kind")
var ErrAdminSettingsCheckConfig = errors.New("invalid settings check configuration")

type AdminSettingsCheckResult struct {
	Success bool
	Message string
}

// CheckAdminSettingsConnection performs one synchronous check. Some providers
// bill requests or write temporary objects; this is not a durable job and callers
// must not automatically replay an uncertain result.
func (h *AdminHandler) CheckAdminSettingsConnection(ctx context.Context, kind string, values map[string]string, dirtyKeys []string) (AdminSettingsCheckResult, error) {
	switch kind {
	case "s3_public", "s3_operational", "s3_private", "redis", "recommendations_embedding", "ai_chat", "ai_transcription", "meilisearch", "mdblist":
	default:
		return AdminSettingsCheckResult{}, ErrAdminSettingsCheckKind
	}
	if h.SettingsRepo == nil {
		return AdminSettingsCheckResult{}, ErrAdminSettingsUnavailable
	}
	effective, err := h.effectiveSettingsForConnectionCheck(ctx, kind, adminSettingsConnectionCheckRequest{Values: values, DirtyKeys: dirtyKeys})
	if err != nil {
		return AdminSettingsCheckResult{}, err
	}
	cfg, err := config.LoadFromDB(effective)
	if err != nil {
		return AdminSettingsCheckResult{}, ErrAdminSettingsCheckConfig
	}
	result, err := runAdminSettingsConnectionCheck(ctx, kind, cfg, effective)
	if err != nil {
		return AdminSettingsCheckResult{}, err
	}
	// Provider errors can include credentials, submitted endpoints, or response
	// bodies. The native boundary returns a stable diagnostic without those values.
	if !result.Success {
		result.Message = result.safeMessage
		if result.Message == "" {
			result.Message = "Connection check failed. Verify the submitted settings and provider availability."
		}
	}
	return AdminSettingsCheckResult{Success: result.Success, Message: result.Message}, nil
}

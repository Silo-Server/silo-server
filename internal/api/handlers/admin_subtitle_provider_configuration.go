package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// These application values deliberately omit stored credential contents.
// Administrator/demo authorization and validator encoding belong to transport.
type AdminSubtitleProviderConfiguration struct {
	ProviderName   string
	Revision       int64
	Enabled        bool
	HasAPIKey      bool
	HasCredentials bool
}

type SubtitleProviderLocalApply string

const (
	SubtitleProviderLocalApplied       SubtitleProviderLocalApply = "applied"
	SubtitleProviderLocalNotConfigured SubtitleProviderLocalApply = "not_configured"
	SubtitleProviderLocalUnsupported   SubtitleProviderLocalApply = "unsupported"
	SubtitleProviderLocalFailed        SubtitleProviderLocalApply = "failed"
)

type AdminSubtitleProviderSaveResult struct {
	SavedRevision int64
	LocalApply    SubtitleProviderLocalApply
	// LocalAppliedRevision identifies the config actually read/applied here. A
	// concurrent writer can make it differ from SavedRevision; zero means absent.
	LocalAppliedRevision *int64
}

func (h *AdminSubtitleHandler) providerConfigurationRepository() (subtitles.ProviderConfigRevisionRepository, error) {
	if h == nil || h.repo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle provider configuration is unavailable")
	}
	repo, ok := h.repo.(subtitles.ProviderConfigRevisionRepository)
	if !ok {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Guarded subtitle provider configuration is unavailable")
	}
	return repo, nil
}
func providerConfigurationError(err error) error {
	if _, ok := errors.AsType[*subtitles.ProviderConfigRevisionConflict](err); ok {
		return err
	}
	if errors.Is(err, subtitles.ErrProviderConfigEncryptionUnavailable) {
		return apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle provider encryption is unavailable")
	}
	return apiError(http.StatusInternalServerError, "internal_error", "Unable to confirm subtitle provider configuration; reconcile it before another attempt")
}
func validSubtitleProviderConfigurationName(name string) bool { return name != "" && len(name) <= 128 }

func (h *AdminSubtitleHandler) GetAdminSubtitleProviderConfiguration(ctx context.Context, name string) (AdminSubtitleProviderConfiguration, error) {
	if !validSubtitleProviderConfigurationName(name) {
		return AdminSubtitleProviderConfiguration{}, apiError(http.StatusBadRequest, "bad_request", "Invalid provider name")
	}
	repo, err := h.providerConfigurationRepository()
	if err != nil {
		return AdminSubtitleProviderConfiguration{}, err
	}
	current, err := repo.GetProviderConfigWithRevision(ctx, name)
	if err != nil {
		return AdminSubtitleProviderConfiguration{}, providerConfigurationError(err)
	}
	view := AdminSubtitleProviderConfiguration{ProviderName: name}
	if current != nil {
		view.Revision = current.Revision
		view.Enabled = current.Config.Enabled
		view.HasAPIKey = current.Config.APIKey != ""
		view.HasCredentials = current.Config.Username != "" && current.Config.Password != ""
	}
	return view, nil
}

// SaveAdminSubtitleProviderConfiguration performs one guarded save. It never
// reloads after a SQL error. After a confirmed save, a local apply failure is
// represented separately and must not be mistaken for failure to persist.
func (h *AdminSubtitleHandler) SaveAdminSubtitleProviderConfiguration(ctx context.Context, name string, change subtitles.ProviderConfigChange, expected *int64) (AdminSubtitleProviderSaveResult, error) {
	if !validSubtitleProviderConfigurationName(name) || len(change.APIKey) > 8192 || len(change.Username) > 1024 || len(change.Password) > 8192 || (expected != nil && *expected < 0) {
		return AdminSubtitleProviderSaveResult{}, apiError(http.StatusBadRequest, "bad_request", "Invalid provider configuration bounds")
	}
	repo, err := h.providerConfigurationRepository()
	if err != nil {
		return AdminSubtitleProviderSaveResult{}, err
	}
	current, err := repo.GetProviderConfigWithRevision(ctx, name)
	if err != nil {
		return AdminSubtitleProviderSaveResult{}, providerConfigurationError(err)
	}
	revision := int64(0)
	var stored *subtitles.ProviderConfig
	if current != nil {
		revision = current.Revision
		stored = &current.Config
	}
	if expected == nil && current == nil || expected != nil && *expected != revision {
		return AdminSubtitleProviderSaveResult{}, &subtitles.ProviderConfigRevisionConflict{CurrentRevision: revision}
	}
	if change.Enabled && !change.ClearCredentials && knownSubtitleProvider(name) {
		if h.providerFactory == nil {
			return AdminSubtitleProviderSaveResult{}, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle provider construction is unavailable")
		}
		draft := preserveSubtitleProviderFields(stored, updateSubtitleProviderRequest{Enabled: change.Enabled, APIKey: change.APIKey, Username: change.Username, Password: change.Password})
		if provider, err := h.providerFactory(subtitleProviderConfigFromRequest(name, draft)); err != nil || provider == nil || provider.Name() != name {
			return AdminSubtitleProviderSaveResult{}, apiError(http.StatusBadRequest, "invalid_provider_config", "Enabled provider configuration is incomplete or invalid")
		}
	}
	saved, err := repo.SaveProviderConfigWithRevision(ctx, name, change, expected)
	if err != nil {
		return AdminSubtitleProviderSaveResult{}, providerConfigurationError(err)
	}
	result := AdminSubtitleProviderSaveResult{SavedRevision: saved}
	switch {
	case !knownSubtitleProvider(name):
		result.LocalApply = SubtitleProviderLocalUnsupported
	case h.manager == nil:
		result.LocalApply = SubtitleProviderLocalNotConfigured
	default:
		applied, err := h.applyProviderConfigurationRevision(ctx, repo, name)
		if err != nil {
			result.LocalApply = SubtitleProviderLocalFailed
		} else {
			result.LocalApply = SubtitleProviderLocalApplied
			result.LocalAppliedRevision = new(applied)
		}
	}
	return result, nil
}

// Share the bridge's local reload lane, but read the revision explicitly. This
// serializes only this handler instance and does not promise cluster convergence.
func (h *AdminSubtitleHandler) applyProviderConfigurationRevision(ctx context.Context, repo subtitles.ProviderConfigRevisionRepository, name string) (int64, error) {
	h.providerReloadMu.Lock()
	defer h.providerReloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	current, err := repo.GetProviderConfigWithRevision(ctx, name)
	if err != nil {
		return 0, err
	}
	revision := int64(0)
	var provider subtitles.Provider
	if current != nil {
		revision = current.Revision
		if current.Config.Enabled {
			if h.providerFactory == nil {
				return 0, errors.New("provider factory unavailable")
			}
			provider, err = h.providerFactory(&current.Config)
			if err != nil {
				return 0, err
			}
			if provider == nil || provider.Name() != name {
				return 0, errors.New("provider identity mismatch")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	h.manager.RemoveProvider(name)
	if provider != nil {
		h.manager.RegisterProvider(provider)
	}
	return revision, nil
}

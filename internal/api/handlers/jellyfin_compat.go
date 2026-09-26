package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

const jellyfinCompatWebOperationUpdatedEvent = "jellyfin_compat.web_operation.updated"

type jellyfinCompatSettingsRequest = AdminJellyfinCompatSettingsPatch

type AdminJellyfinCompatSettingsPatch struct {
	Enabled               *bool   `json:"enabled,omitempty"`
	PublicURL             *string `json:"public_url,omitempty"`
	ServerName            *string `json:"server_name,omitempty"`
	EmulatedServerVersion *string `json:"emulated_server_version,omitempty"`
	WebEnabled            *bool   `json:"web_enabled,omitempty"`
	WebVersion            *string `json:"web_version,omitempty"`
	WebDir                *string `json:"web_dir,omitempty"`
	WebInstallDir         *string `json:"web_install_dir,omitempty"`
}

type jellyfinCompatWebInstallRequest struct {
	Version   string `json:"version,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
}

// HandleGetJellyfinCompatStatus handles GET /admin/jellyfin-compat/status.
func (h *AdminHandler) HandleGetJellyfinCompatStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.ReadAdminJellyfinCompatStatus(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// ReadAdminJellyfinCompatStatus observes the existing local installation state.
func (h *AdminHandler) ReadAdminJellyfinCompatStatus(ctx context.Context) (jellycompat.WebComponentStatus, error) {
	if h.SettingsRepo == nil {
		return jellycompat.WebComponentStatus{}, apiError(500, "internal_error", "Settings store not configured")
	}
	settings, err := h.SettingsRepo.GetAll(ctx)
	if err != nil {
		return jellycompat.WebComponentStatus{}, apiError(500, "internal_error", "Failed to load settings")
	}
	return jellycompat.WebComponentStatusForConfig(h.Config, settings), nil
}

// HandleUpdateJellyfinCompatSettings handles PATCH /admin/jellyfin-compat/settings.
func (h *AdminHandler) HandleUpdateJellyfinCompatSettings(w http.ResponseWriter, r *http.Request) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store not configured")
		return
	}
	var req jellyfinCompatSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	var stored map[string]string
	if req.WebDir != nil && (req.WebInstallDir == nil || strings.TrimSpace(*req.WebInstallDir) == "") {
		var ok bool
		stored, ok = h.jellyfinCompatSettings(w, r)
		if !ok {
			return
		}
	}
	updates, err := jellyfinCompatSettingUpdates(req, stored)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	for key, value := range updates {
		if err := h.SettingsRepo.Set(r.Context(), key, value); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update setting")
			return
		}
		h.publishSettingChanged(r, key, value)
	}
	if jellyfinCompatSettingsRequireRestart(updates) {
		h.markServerRestartRequired("jellyfin_compat")
	}
	settings, ok := h.jellyfinCompatSettings(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, jellycompat.WebComponentStatusForConfig(h.Config, settings))
}

func jellyfinCompatSettingUpdates(req AdminJellyfinCompatSettingsPatch, settings map[string]string) (map[string]string, error) {
	updates := map[string]string{}
	if req.Enabled != nil {
		if *req.Enabled {
			updates["jellyfin_compat.enabled"] = "true"
		} else {
			updates["jellyfin_compat.enabled"] = "false"
		}
	}
	setOptionalString(updates, "jellyfin_compat.public_url", req.PublicURL)
	setOptionalString(updates, "jellyfin_compat.server_name", req.ServerName)
	setOptionalString(updates, "jellyfin_compat.emulated_server_version", req.EmulatedServerVersion)
	if req.WebEnabled != nil {
		updates["jellyfin_compat.web_enabled"] = strconv.FormatBool(*req.WebEnabled)
	}
	if req.Enabled != nil && !*req.Enabled {
		updates["jellyfin_compat.web_enabled"] = "false"
	}
	setOptionalString(updates, "jellyfin_compat.web_version", req.WebVersion)
	setOptionalString(updates, "jellyfin_compat.web_install_dir", req.WebInstallDir)
	if req.WebDir != nil {
		root := strings.TrimSpace(updates["jellyfin_compat.web_install_dir"])
		if root == "" {
			root = strings.TrimSpace(settings["jellyfin_compat.web_install_dir"])
		}
		if root == "" {
			root = config.DefaultJellyfinWebInstallDir
		}
		managedPath := jellycompat.ManagedWebInstallPath(root)
		if raw := strings.TrimSpace(*req.WebDir); raw != "" && filepath.Clean(raw) != filepath.Clean(managedPath) {
			return nil, apiError(400, "bad_request", "Jellyfin Web active directory is managed by Silo and cannot point at an arbitrary path")
		}
		updates["jellyfin_compat.web_dir"] = managedPath
	}
	if len(updates) == 0 {
		return nil, apiError(400, "bad_request", "At least one setting is required")
	}
	return updates, nil
}

// UpdateAdminJellyfinCompatSettings guards and applies the complete v2 patch.
// The frozen bridge retains its historical per-key write boundary.
func (h *AdminHandler) UpdateAdminJellyfinCompatSettings(ctx context.Context, req AdminJellyfinCompatSettingsPatch, guard func(AdminSettingsSnapshot) error) (jellycompat.WebComponentStatus, error) {
	if h.SettingsRepo == nil {
		return jellycompat.WebComponentStatus{}, ErrAdminSettingsUnavailable
	}
	var updates, committed map[string]string
	var refusal error
	err := updateServerSettingsAtomically(ctx, h.SettingsRepo, func(stored map[string]string) (map[string]string, error) {
		if err := guard(h.adminSettingsSnapshot(stored)); err != nil {
			refusal = err
			return nil, err
		}
		var err error
		updates, err = jellyfinCompatSettingUpdates(req, stored)
		if err != nil {
			refusal = err
			return nil, err
		}
		committed = maps.Clone(stored)
		maps.Copy(committed, updates)
		return updates, nil
	})
	if refusal != nil {
		return jellycompat.WebComponentStatus{}, refusal
	}
	if err != nil {
		return jellycompat.WebComponentStatus{}, err
	}
	for _, key := range slices.Sorted(maps.Keys(updates)) {
		h.publishSettingChangedContext(ctx, key, updates[key])
	}
	if jellyfinCompatSettingsRequireRestart(updates) {
		h.markServerRestartRequired("jellyfin_compat")
	}
	return jellycompat.WebComponentStatusForConfig(h.Config, committed), nil
}

func jellyfinCompatSettingsRequireRestart(updates map[string]string) bool {
	for key := range updates {
		if config.RestartRequired(key) {
			return true
		}
	}
	return false
}

// HandleInstallJellyfinCompatWeb handles POST /admin/jellyfin-compat/web/install.
func (h *AdminHandler) HandleInstallJellyfinCompatWeb(w http.ResponseWriter, r *http.Request) {
	h.installJellyfinCompatWeb(w, r)
}

// HandleUpdateJellyfinCompatWeb handles POST /admin/jellyfin-compat/web/update.
func (h *AdminHandler) HandleUpdateJellyfinCompatWeb(w http.ResponseWriter, r *http.Request) {
	h.installJellyfinCompatWeb(w, r)
}

// HandleRemoveJellyfinCompatWeb handles POST /admin/jellyfin-compat/web/remove.
func (h *AdminHandler) HandleRemoveJellyfinCompatWeb(w http.ResponseWriter, r *http.Request) {
	status, err := h.StartAdminJellyfinCompatWebRemove(r.Context())
	if err != nil {
		writeJellyfinCompatWebStartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

func (h *AdminHandler) installJellyfinCompatWeb(w http.ResponseWriter, r *http.Request) {
	var req jellyfinCompatWebInstallRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			return
		}
	}
	status, err := h.StartAdminJellyfinCompatWebInstall(r.Context(), req)
	if err != nil {
		writeJellyfinCompatWebStartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

// writeJellyfinCompatWebStartError keeps the frozen bridge answers: the seam's
// own decisions arrive as *APIError, installer refusals as jellycompat errors.
func writeJellyfinCompatWebStartError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	writeJellyfinCompatOperationError(w, err)
}

// AdminJellyfinWebInstallRequest pins the Jellyfin Web release to install. An
// empty version resolves the release compatible with the emulated server
// version; an empty source URL uses the stored one.
type AdminJellyfinWebInstallRequest = jellyfinCompatWebInstallRequest

// StartAdminJellyfinCompatWebInstall begins the local, in-process install of
// the Jellyfin Web assets and returns the accepted status. It is the v1 install
// and update behavior: one operation per install root at a time, progress on
// the settings event channel, no durable cluster-wide job. When another
// operation already runs, the returned status carries it alongside
// jellycompat.ErrWebComponentOperationActive so the caller can tell a repeat of
// the same command from a real conflict.
func (h *AdminHandler) StartAdminJellyfinCompatWebInstall(ctx context.Context, req AdminJellyfinWebInstallRequest) (jellycompat.WebComponentStatus, error) {
	settings, err := h.loadJellyfinCompatSettings(ctx)
	if err != nil {
		return jellycompat.WebComponentStatus{}, err
	}
	root := strings.TrimSpace(settings["jellyfin_compat.web_install_dir"])
	if root == "" {
		root = config.DefaultJellyfinWebInstallDir
	}
	sourceURL := strings.TrimSpace(req.SourceURL)
	if sourceURL == "" {
		sourceURL = strings.TrimSpace(settings["jellyfin_compat.web_source_url"])
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = strings.TrimSpace(settings["jellyfin_compat.web_version"])
	}
	if version == "" {
		emulatedVersion := strings.TrimSpace(settings["jellyfin_compat.emulated_server_version"])
		if emulatedVersion == "" && h.Config != nil {
			emulatedVersion = h.Config.JellyfinCompat.EmulatedServerVersion
		}
		if emulatedVersion == "" {
			emulatedVersion = config.DefaultJellyfinCompatEmulatedServerVersion
		}
		resolvedVersion, err := jellycompat.ResolveCompatibleWebVersion(ctx, sourceURL, emulatedVersion)
		if err != nil {
			return jellycompat.WebComponentStatus{}, apiError(http.StatusServiceUnavailable, "web_version_unavailable", err.Error())
		}
		version = resolvedVersion
	}
	return jellycompat.StartWebComponentInstall(jellycompat.WebComponentInstallOptions{
		InstallRoot: root,
		SourceURL:   sourceURL,
		Version:     version,
		OnProgress:  h.publishJellyfinCompatWebOperationProgress,
	}, func(ctx context.Context, status jellycompat.WebComponentStatus) error {
		return h.persistJellyfinCompatWebInstallSettings(ctx, status)
	})
}

// StartAdminJellyfinCompatWebRemove begins the local removal of the managed
// Jellyfin Web assets, records web_enabled=false and returns the accepted
// status. Same operation-per-root and conflict semantics as install.
func (h *AdminHandler) StartAdminJellyfinCompatWebRemove(ctx context.Context) (jellycompat.WebComponentStatus, error) {
	settings, err := h.loadJellyfinCompatSettings(ctx)
	if err != nil {
		return jellycompat.WebComponentStatus{}, err
	}
	root := strings.TrimSpace(settings["jellyfin_compat.web_install_dir"])
	if root == "" {
		root = config.DefaultJellyfinWebInstallDir
	}
	status, err := jellycompat.StartWebComponentRemove(jellycompat.WebComponentRemoveOptions{
		InstallRoot: root,
		OnProgress:  h.publishJellyfinCompatWebOperationProgress,
	})
	if err != nil {
		return status, err
	}
	if err := h.persistJellyfinCompatWebEnabled(ctx, false); err != nil {
		return jellycompat.WebComponentStatus{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update setting")
	}
	settings, err = h.loadJellyfinCompatSettings(ctx)
	if err != nil {
		return jellycompat.WebComponentStatus{}, err
	}
	return jellycompat.WebComponentStatusForConfig(h.Config, settings), nil
}

func (h *AdminHandler) loadJellyfinCompatSettings(ctx context.Context) (map[string]string, error) {
	if h.SettingsRepo == nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Settings store not configured")
	}
	settings, err := h.SettingsRepo.GetAll(ctx)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load settings")
	}
	return settings, nil
}

func (h *AdminHandler) persistJellyfinCompatWebInstallSettings(ctx context.Context, status jellycompat.WebComponentStatus) error {
	if h.SettingsRepo == nil {
		return nil
	}
	updates := map[string]string{
		"jellyfin_compat.web_enabled":     "true",
		"jellyfin_compat.web_install_dir": status.InstallRoot,
		"jellyfin_compat.web_dir":         jellycompat.ManagedWebInstallPath(status.InstallRoot),
		"jellyfin_compat.web_version":     status.PinnedVersion,
	}
	if status.SourceURL != "" {
		updates["jellyfin_compat.web_source_url"] = status.SourceURL
	}
	for key, value := range updates {
		if err := h.SettingsRepo.Set(ctx, key, value); err != nil {
			return fmt.Errorf("persist Jellyfin Web setting %s: %w", key, err)
		}
		h.publishSettingChangedContext(ctx, key, value)
	}
	return nil
}

func (h *AdminHandler) persistJellyfinCompatWebEnabled(ctx context.Context, enabled bool) error {
	if h.SettingsRepo == nil {
		return nil
	}
	value := strconv.FormatBool(enabled)
	if err := h.SettingsRepo.Set(ctx, "jellyfin_compat.web_enabled", value); err != nil {
		return fmt.Errorf("persist Jellyfin Web setting jellyfin_compat.web_enabled: %w", err)
	}
	h.publishSettingChangedContext(ctx, "jellyfin_compat.web_enabled", value)
	return nil
}

func (h *AdminHandler) publishJellyfinCompatWebOperationProgress(op jellycompat.WebComponentOperationStatus) {
	if h == nil || h.EventsHub == nil {
		return
	}
	_ = h.EventsHub.PublishJSON(
		context.Background(),
		evt.ChannelSettings,
		jellyfinCompatWebOperationUpdatedEvent,
		op,
		evt.PublishOptions{AdminOnly: true},
	)
}

func (h *AdminHandler) jellyfinCompatSettings(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store not configured")
		return nil, false
	}
	settings, err := h.SettingsRepo.GetAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load settings")
		return nil, false
	}
	return settings, true
}

func setOptionalString(updates map[string]string, key string, value *string) {
	if value == nil {
		return
	}
	updates[key] = strings.TrimSpace(*value)
}

func (h *AdminHandler) publishSettingChanged(r *http.Request, key, value string) {
	h.publishSettingChangedContext(r.Context(), key, value)
}

func (h *AdminHandler) publishSettingChangedContext(ctx context.Context, key, value string) {
	if h.EventBus != nil {
		_ = h.EventBus.Publish(ctx, cache.ChannelAdmin,
			cache.Event{Type: cache.EventSettingsChanged, Payload: key})
	}
	if h.OnServerSettingUpdated != nil {
		h.OnServerSettingUpdated(ctx, key, value)
	}
}

func writeJellyfinCompatOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jellycompat.ErrWebComponentOperationActive):
		writeError(w, http.StatusConflict, "operation_active", "A Jellyfin Web operation is already running")
	case errors.Is(err, jellycompat.ErrWebInstallerUnavailable):
		writeError(w, http.StatusServiceUnavailable, "installer_unavailable", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

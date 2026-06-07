package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

type jellyfinCompatSettingsRequest struct {
	Enabled               *bool   `json:"enabled,omitempty"`
	PublicURL             *string `json:"public_url,omitempty"`
	ServerName            *string `json:"server_name,omitempty"`
	EmulatedServerVersion *string `json:"emulated_server_version,omitempty"`
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
	settings, ok := h.jellyfinCompatSettings(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, jellycompat.WebComponentStatusForConfig(h.Config, settings))
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
	setOptionalString(updates, "jellyfin_compat.web_version", req.WebVersion)
	setOptionalString(updates, "jellyfin_compat.web_install_dir", req.WebInstallDir)
	if req.WebDir != nil {
		root := strings.TrimSpace(updates["jellyfin_compat.web_install_dir"])
		if root == "" {
			settings, ok := h.jellyfinCompatSettings(w, r)
			if !ok {
				return
			}
			root = strings.TrimSpace(settings["jellyfin_compat.web_install_dir"])
		}
		if root == "" {
			root = config.DefaultJellyfinWebInstallDir
		}
		managedPath := jellycompat.ManagedWebInstallPath(root)
		if raw := strings.TrimSpace(*req.WebDir); raw != "" && filepath.Clean(raw) != filepath.Clean(managedPath) {
			writeError(w, http.StatusBadRequest, "bad_request", "Jellyfin Web active directory is managed by Silo and cannot point at an arbitrary path")
			return
		}
		updates["jellyfin_compat.web_dir"] = managedPath
	}
	if len(updates) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "At least one setting is required")
		return
	}
	for key, value := range updates {
		if err := h.SettingsRepo.Set(r.Context(), key, value); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update setting")
			return
		}
		h.publishSettingChanged(r, key, value)
	}
	settings, ok := h.jellyfinCompatSettings(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, jellycompat.WebComponentStatusForConfig(h.Config, settings))
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
	settings, ok := h.jellyfinCompatSettings(w, r)
	if !ok {
		return
	}
	root := strings.TrimSpace(settings["jellyfin_compat.web_install_dir"])
	if root == "" {
		root = config.DefaultJellyfinWebInstallDir
	}
	_, err := jellycompat.StartWebComponentRemove(root)
	if err != nil {
		writeJellyfinCompatOperationError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jellycompat.WebComponentStatusForConfig(h.Config, settings))
}

func (h *AdminHandler) installJellyfinCompatWeb(w http.ResponseWriter, r *http.Request) {
	settings, ok := h.jellyfinCompatSettings(w, r)
	if !ok {
		return
	}
	var req jellyfinCompatWebInstallRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			return
		}
	}
	root := strings.TrimSpace(settings["jellyfin_compat.web_install_dir"])
	if root == "" {
		root = config.DefaultJellyfinWebInstallDir
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = strings.TrimSpace(settings["jellyfin_compat.web_version"])
	}
	if version == "" {
		version = config.DefaultJellyfinWebVersion
	}
	sourceURL := strings.TrimSpace(req.SourceURL)
	if sourceURL == "" {
		sourceURL = strings.TrimSpace(settings["jellyfin_compat.web_source_url"])
	}
	_, err := jellycompat.StartWebComponentInstall(jellycompat.WebComponentInstallOptions{
		InstallRoot: root,
		SourceURL:   sourceURL,
		Version:     version,
	}, func(ctx context.Context, status jellycompat.WebComponentStatus) error {
		if h.SettingsRepo == nil {
			return nil
		}
		updates := map[string]string{
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
	})
	if err != nil {
		writeJellyfinCompatOperationError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jellycompat.WebComponentStatusForConfig(h.Config, settings))
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

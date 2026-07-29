// internal/api/handlers/admin_subtitles.go
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/opensubtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subdl"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsource"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubtitleProviderFactory creates a Provider from a config. Allows testing without real providers.
type SubtitleProviderFactory func(cfg *subtitles.ProviderConfig) (subtitles.Provider, error)

// AdminSubtitleHandler handles admin operations for subtitle provider management.
type AdminSubtitleHandler struct {
	repo             subtitles.Repository
	manager          *subtitles.Manager
	pool             *pgxpool.Pool
	providerFactory  SubtitleProviderFactory
	providerReloadMu sync.Mutex
}

// NewAdminSubtitleHandler creates a new AdminSubtitleHandler.
func NewAdminSubtitleHandler(repo subtitles.Repository) *AdminSubtitleHandler {
	return &AdminSubtitleHandler{
		repo:            repo,
		providerFactory: defaultProviderFactory,
	}
}

type updateSubtitleProviderRequest struct {
	Enabled          bool   `json:"enabled"`
	APIKey           string `json:"api_key"`
	Username         string `json:"username"`
	Password         string `json:"password"`
	ClearCredentials bool   `json:"clear_credentials"`
}

type subtitleProviderCredentialClearer interface {
	ClearProviderCredentials(ctx context.Context, providerName string) error
}

var builtinSubtitleProviders = []string{"opensubtitles", "subdl", "subsource"}

// HandleListProviders handles GET /api/v1/admin/subtitle-providers
func (h *AdminSubtitleHandler) HandleListProviders(w http.ResponseWriter, r *http.Request) {
	configs, err := h.repo.ListProviderConfigs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_error", "Failed to list providers")
		return
	}
	configs = mergeSubtitleProviderConfigs(configs)
	writeJSON(w, http.StatusOK, map[string]interface{}{"providers": configs})
}

// HandleUpdateProvider handles PUT /api/v1/admin/subtitle-providers/{provider}
func (h *AdminSubtitleHandler) HandleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	providerName, err := decodedURLParam(r, "provider")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid provider name")
		return
	}
	knownProvider := knownSubtitleProvider(providerName)

	var req updateSubtitleProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}
	if req.ClearCredentials {
		clearer, ok := h.repo.(subtitleProviderCredentialClearer)
		if !ok {
			writeError(w, http.StatusInternalServerError, "update_error", "Provider credential clearing is not supported")
			return
		}
		if err := clearer.ClearProviderCredentials(r.Context(), providerName); err != nil {
			writeError(w, http.StatusInternalServerError, "update_error", "Failed to clear provider credentials")
			return
		}
		if knownProvider {
			if err := h.reloadProviderFromRepository(r.Context(), providerName); err != nil {
				writeError(w, http.StatusInternalServerError, "update_error", "Credentials cleared but latest provider config could not be applied")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{ //nolint:goconst // JSON response keys stay inline.
			"status": "ok", "applied_live": knownProvider && h.manager != nil,
		})
		return
	}

	stored, err := h.repo.GetProviderConfig(r.Context(), providerName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update_error", "Failed to load provider config")
		return
	}
	req = preserveSubtitleProviderFields(stored, req)

	if req.Enabled && knownProvider {
		_, err = h.providerFactory(subtitleProviderConfigFromRequest(providerName, req))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_provider_config", err.Error())
			return
		}
	}

	cfg := &subtitles.ProviderConfig{
		ProviderName: providerName,
		Enabled:      req.Enabled,
		APIKey:       req.APIKey,
		Username:     req.Username,
		Password:     req.Password,
	}

	if err := h.repo.UpsertProviderConfig(r.Context(), cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "update_error", "Failed to update provider config")
		return
	}

	if knownProvider {
		if err := h.reloadProviderFromRepository(r.Context(), providerName); err != nil {
			writeError(w, http.StatusInternalServerError, "update_error", "Provider saved but latest config could not be applied")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok", "applied_live": knownProvider && h.manager != nil,
	})
}

// reloadProviderFromRepository serializes the read-and-apply phase and always
// rebuilds from durable state. A concurrent request may commit after this
// request, but its own reload cannot be overtaken by an older request applying
// request-local state afterward.
func (h *AdminSubtitleHandler) reloadProviderFromRepository(ctx context.Context, providerName string) error {
	if h.manager == nil {
		return nil
	}

	h.providerReloadMu.Lock()
	defer h.providerReloadMu.Unlock()

	cfg, err := h.repo.GetProviderConfig(ctx, providerName)
	if err != nil {
		return fmt.Errorf("load latest provider config: %w", err)
	}

	var provider subtitles.Provider
	if cfg != nil && cfg.Enabled {
		provider, err = h.providerFactory(cfg)
		if err != nil {
			return fmt.Errorf("create provider from latest config: %w", err)
		}
	}

	h.manager.RemoveProvider(providerName)
	if provider != nil {
		h.manager.RegisterProvider(provider)
	}
	return nil
}

// HandleTestProvider handles POST /api/v1/admin/subtitle-providers/{provider}/test
func (h *AdminSubtitleHandler) HandleTestProvider(w http.ResponseWriter, r *http.Request) {
	providerName, err := decodedURLParam(r, "provider")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid provider name")
		return
	}

	var req updateSubtitleProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}

	stored, err := h.repo.GetProviderConfig(r.Context(), providerName)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "error": fmt.Sprintf("Failed to load config: %v", err),
		})
		return
	}
	if stored == nil && req.APIKey == "" && req.Username == "" && req.Password == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "error": "Provider credentials are not configured",
		})
		return
	}
	req = preserveSubtitleProviderFields(stored, req)
	cfg := subtitleProviderConfigFromRequest(providerName, req)

	provider, err := h.providerFactory(cfg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "error": err.Error(),
		})
		return
	}

	// Do a test search with a well-known title.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	results, err := provider.Search(ctx, subtitles.SearchRequest{
		Title:     "The Matrix",
		Year:      1999,
		Languages: []string{"en"},
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "error": err.Error(),
		})
		return
	}

	if len(results) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "error": "Search returned no results (credentials may be invalid)",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}

func preserveSubtitleProviderFields(stored *subtitles.ProviderConfig, req updateSubtitleProviderRequest) updateSubtitleProviderRequest {
	if stored == nil {
		return req
	}
	preserveEmpty := func(draft *string, current string) {
		if *draft == "" {
			*draft = current
		}
	}
	preserveEmpty(&req.APIKey, stored.APIKey)
	preserveEmpty(&req.Username, stored.Username)
	preserveEmpty(&req.Password, stored.Password)
	return req
}

func subtitleProviderConfigFromRequest(providerName string, req updateSubtitleProviderRequest) *subtitles.ProviderConfig {
	return &subtitles.ProviderConfig{
		ProviderName: providerName,
		Enabled:      req.Enabled,
		APIKey:       req.APIKey,
		Username:     req.Username,
		Password:     req.Password,
	}
}

func knownSubtitleProvider(name string) bool {
	for _, providerName := range builtinSubtitleProviders {
		if name == providerName {
			return true
		}
	}
	return false
}

func defaultProviderFactory(cfg *subtitles.ProviderConfig) (subtitles.Provider, error) {
	switch cfg.ProviderName {
	case "opensubtitles":
		if cfg.Username == "" || cfg.Password == "" {
			return nil, fmt.Errorf("Username and password are required")
		}
		return opensubtitles.New(opensubtitles.Config{
			Username: cfg.Username,
			Password: cfg.Password,
		}), nil
	case "subdl":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("API key is required")
		}
		return subdl.New(subdl.Config{APIKey: cfg.APIKey}), nil
	case "subsource":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("API key is required")
		}
		return subsource.New(subsource.Config{APIKey: cfg.APIKey}), nil
	default:
		return nil, fmt.Errorf("Unknown provider: %s", cfg.ProviderName)
	}
}

func mergeSubtitleProviderConfigs(configs []subtitles.ProviderConfig) []subtitles.ProviderConfig {
	if len(configs) == 0 {
		merged := make([]subtitles.ProviderConfig, 0, len(builtinSubtitleProviders))
		for _, providerName := range builtinSubtitleProviders {
			merged = append(merged, subtitles.ProviderConfig{ProviderName: providerName})
		}
		return merged
	}

	byName := make(map[string]subtitles.ProviderConfig, len(configs))
	for _, cfg := range configs {
		byName[cfg.ProviderName] = cfg
	}

	merged := make([]subtitles.ProviderConfig, 0, len(builtinSubtitleProviders))
	for _, providerName := range builtinSubtitleProviders {
		if cfg, ok := byName[providerName]; ok {
			merged = append(merged, cfg)
			delete(byName, providerName)
			continue
		}
		merged = append(merged, subtitles.ProviderConfig{ProviderName: providerName})
	}

	for _, cfg := range configs {
		if _, ok := byName[cfg.ProviderName]; ok {
			merged = append(merged, cfg)
		}
	}

	return merged
}

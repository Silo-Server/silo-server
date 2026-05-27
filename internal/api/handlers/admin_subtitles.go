// internal/api/handlers/admin_subtitles.go
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/opensubtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subdl"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsource"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubtitleProviderFactory creates a Provider from a config. Allows testing without real providers.
type SubtitleProviderFactory func(cfg *subtitles.ProviderConfig) (subtitles.Provider, error)

// AdminSubtitleHandler handles admin operations for subtitle provider management.
type AdminSubtitleHandler struct {
	repo            subtitles.Repository
	manager         *subtitles.Manager
	pool            *pgxpool.Pool
	providerFactory SubtitleProviderFactory
}

// NewAdminSubtitleHandler creates a new AdminSubtitleHandler.
func NewAdminSubtitleHandler(repo subtitles.Repository) *AdminSubtitleHandler {
	return &AdminSubtitleHandler{
		repo:            repo,
		providerFactory: defaultProviderFactory,
	}
}

type updateSubtitleProviderRequest struct {
	Enabled  bool   `json:"enabled"`
	APIKey   string `json:"api_key"`
	Username string `json:"username"`
	Password string `json:"password"`
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
	providerName := chi.URLParam(r, "provider")

	var req updateSubtitleProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
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

	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

// HandleTestProvider handles POST /api/v1/admin/subtitle-providers/{provider}/test
func (h *AdminSubtitleHandler) HandleTestProvider(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")

	cfg, err := h.repo.GetProviderConfig(r.Context(), providerName)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "error": fmt.Sprintf("Failed to load config: %v", err),
		})
		return
	}
	if cfg == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "error": "Provider not found",
		})
		return
	}

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

// internal/api/handlers/admin_subtitles.go
package handlers

import (
	"context"
	"fmt"
	"sync"

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

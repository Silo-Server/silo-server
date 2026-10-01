package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const subtitleProviderTestTitle = "The Matrix"

type SubtitleProviderTestConfig struct {
	Enabled  bool
	APIKey   string
	Username string
	Password string
}
type SubtitleProviderTestView struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func (h *AdminSubtitleHandler) ListSubtitleProviderConfigs(ctx context.Context) ([]subtitles.ProviderConfig, error) {
	if h == nil || h.repo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle providers are not configured")
	}
	configs, err := h.repo.ListProviderConfigs(ctx)
	if err != nil {
		return nil, err
	}
	return mergeSubtitleProviderConfigs(configs), nil
}

// TestSubtitleProvider does one bounded search without persisting supplied credentials.
func (h *AdminSubtitleHandler) TestSubtitleProvider(ctx context.Context, providerName string, input SubtitleProviderTestConfig) SubtitleProviderTestView {
	if h == nil || h.repo == nil || h.providerFactory == nil {
		return SubtitleProviderTestView{Error: "Subtitle providers are not configured"}
	}
	req := updateSubtitleProviderRequest{Enabled: input.Enabled, APIKey: input.APIKey, Username: input.Username, Password: input.Password}
	stored, err := h.repo.GetProviderConfig(ctx, providerName)
	if err != nil {
		return SubtitleProviderTestView{Error: fmt.Sprintf("Failed to load config: %v", err)}
	}
	if stored == nil && req.APIKey == "" && req.Username == "" && req.Password == "" {
		return SubtitleProviderTestView{Error: "Provider credentials are not configured"}
	}
	req = preserveSubtitleProviderFields(stored, req)
	cfg := subtitleProviderConfigFromRequest(providerName, req)

	provider, err := h.providerFactory(cfg)
	if err != nil {
		return SubtitleProviderTestView{Error: err.Error()}
	}

	// Do a test search with a well-known title.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	results, err := provider.Search(ctx, subtitles.SearchRequest{
		Title:     subtitleProviderTestTitle,
		Year:      1999,
		Languages: []string{"en"},
	})
	if err != nil {
		return SubtitleProviderTestView{Error: err.Error()}
	}

	if len(results) == 0 {
		return SubtitleProviderTestView{Error: "Search returned no results (credentials may be invalid)"}
	}

	return SubtitleProviderTestView{Success: true}
}

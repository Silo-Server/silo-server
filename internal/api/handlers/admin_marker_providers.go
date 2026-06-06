package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/markers"
)

// AdminMarkerProvidersHandler serves the per-provider marker config + key
// validation API under the RequireAdmin group.
type AdminMarkerProvidersHandler struct {
	Registry *markers.Registry
	Config   *markers.ProviderConfigStore
	logger   *slog.Logger
}

// NewAdminMarkerProvidersHandler constructs the handler.
func NewAdminMarkerProvidersHandler(registry *markers.Registry, config *markers.ProviderConfigStore, logger *slog.Logger) *AdminMarkerProvidersHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminMarkerProvidersHandler{Registry: registry, Config: config, logger: logger}
}

type providerConfigResponse struct {
	Provider                string  `json:"provider"`
	IsSubmitter             bool    `json:"is_submitter"`
	FetchEnabled            bool    `json:"fetch_enabled"`
	FetchPriority           int     `json:"fetch_priority"`
	ContributeEnabled       bool    `json:"contribute_enabled"`
	ContributeAutoLocal     bool    `json:"contribute_auto_local"`
	ContributeMinConfidence float64 `json:"contribute_min_confidence"`
}

func (h *AdminMarkerProvidersHandler) submitterIDs() map[string]bool {
	out := map[string]bool{}
	if h.Registry == nil {
		return out
	}
	for _, p := range h.Registry.Providers() {
		if _, ok := p.(markers.Submitter); ok {
			out[p.ID()] = true
		}
	}
	return out
}

// HandleListProviders lists registered providers with their config + capability.
func (h *AdminMarkerProvidersHandler) HandleListProviders(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker providers are not configured")
		return
	}
	submitters := h.submitterIDs()
	out := []providerConfigResponse{}
	for _, c := range h.Config.List() {
		out = append(out, toProviderConfigResponse(c, submitters[c.Provider]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// HandleUpdateProvider updates a provider's config row.
func (h *AdminMarkerProvidersHandler) HandleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker providers are not configured")
		return
	}
	provider := chi.URLParam(r, "provider")
	existing, ok := h.Config.Get(provider)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Unknown marker provider")
		return
	}
	var body struct {
		FetchEnabled            *bool    `json:"fetch_enabled"`
		FetchPriority           *int     `json:"fetch_priority"`
		ContributeEnabled       *bool    `json:"contribute_enabled"`
		ContributeAutoLocal     *bool    `json:"contribute_auto_local"`
		ContributeMinConfidence *float64 `json:"contribute_min_confidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if body.FetchEnabled != nil {
		existing.FetchEnabled = *body.FetchEnabled
	}
	if body.FetchPriority != nil {
		existing.FetchPriority = *body.FetchPriority
	}
	if body.ContributeEnabled != nil {
		existing.ContributeEnabled = *body.ContributeEnabled
	}
	if body.ContributeAutoLocal != nil {
		existing.ContributeAutoLocal = *body.ContributeAutoLocal
	}
	if body.ContributeMinConfidence != nil {
		v := *body.ContributeMinConfidence
		if v < 0 || v > 1 {
			writeError(w, http.StatusBadRequest, "bad_request", "contribute_min_confidence must be between 0 and 1")
			return
		}
		existing.ContributeMinConfidence = v
	}
	if err := h.Config.Update(r.Context(), existing); err != nil {
		h.logger.Error("admin markers: update provider config failed", "provider", provider, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update provider")
		return
	}
	writeJSON(w, http.StatusOK, toProviderConfigResponse(existing, h.submitterIDs()[provider]))
}

// HandleValidateProvider validates the provider's configured key and returns stats.
func (h *AdminMarkerProvidersHandler) HandleValidateProvider(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Registry == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker providers are not configured")
		return
	}
	provider := chi.URLParam(r, "provider")
	var submitter markers.Submitter
	for _, p := range h.Registry.Providers() {
		if p.ID() == provider {
			if s, ok := p.(markers.Submitter); ok {
				submitter = s
			}
		}
	}
	if submitter == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Provider does not support contribution")
		return
	}
	stats, err := submitter.FetchUserStats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "stats": stats})
}

func toProviderConfigResponse(c markers.ProviderConfig, isSubmitter bool) providerConfigResponse {
	return providerConfigResponse{
		Provider:                c.Provider,
		IsSubmitter:             isSubmitter,
		FetchEnabled:            c.FetchEnabled,
		FetchPriority:           c.FetchPriority,
		ContributeEnabled:       c.ContributeEnabled,
		ContributeAutoLocal:     c.ContributeAutoLocal,
		ContributeMinConfidence: c.ContributeMinConfidence,
	}
}

package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/markers"
)

// AdminMarkerProvidersHandler serves the per-provider marker config + key
// validation API under the RequireAdmin group.
type AdminMarkerProvidersHandler struct {
	Registry *markers.Registry
	Config   *markers.ProviderConfigStore
	EventBus cache.EventBus
	logger   *slog.Logger
}

// NewAdminMarkerProvidersHandler constructs the handler.
func NewAdminMarkerProvidersHandler(registry *markers.Registry, config *markers.ProviderConfigStore, eventBus cache.EventBus, logger *slog.Logger) *AdminMarkerProvidersHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminMarkerProvidersHandler{Registry: registry, Config: config, EventBus: eventBus, logger: logger}
}

type providerConfigResponse struct {
	Provider                string  `json:"provider"`
	DisplayName             string  `json:"display_name,omitempty"`
	SourceType              string  `json:"source_type,omitempty"`
	PluginID                string  `json:"plugin_id,omitempty"`
	PluginInstallationID    int     `json:"plugin_installation_id,omitempty"`
	CapabilityID            string  `json:"capability_id,omitempty"`
	IsSubmitter             bool    `json:"is_submitter"`
	FetchEnabled            bool    `json:"fetch_enabled"`
	FetchPriority           int     `json:"fetch_priority"`
	ContributeEnabled       bool    `json:"contribute_enabled"`
	ContributeAutoLocal     bool    `json:"contribute_auto_local"`
	ContributeMinConfidence float64 `json:"contribute_min_confidence"`
}

type markerUserStatsResponse struct {
	Total          int     `json:"total"`
	Accepted       int     `json:"accepted"`
	Pending        int     `json:"pending"`
	Rejected       int     `json:"rejected"`
	AcceptanceRate float64 `json:"acceptance_rate"`
	CurrentStreak  int     `json:"current_streak"`
	BestStreak     int     `json:"best_streak"`
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

func (h *AdminMarkerProvidersHandler) providerDescriptions() map[string]markers.ProviderDescriptor {
	out := map[string]markers.ProviderDescriptor{}
	if h.Registry == nil {
		return out
	}
	for _, p := range h.Registry.Providers() {
		desc := markers.ProviderDescriptor{ID: p.ID()}
		if described, ok := p.(markers.DescribedProvider); ok {
			desc = described.ProviderDescription()
		}
		if desc.ID == "" {
			desc.ID = p.ID()
		}
		out[p.ID()] = desc
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
	descriptions := h.providerDescriptions()
	out := []providerConfigResponse{}
	if h.Registry != nil {
		for _, provider := range h.Registry.Providers() {
			c, ok := h.Config.Get(provider.ID())
			if !ok {
				continue
			}
			out = append(out, toProviderConfigResponse(c, submitters[c.Provider], descriptions[c.Provider]))
		}
	} else {
		for _, c := range h.Config.List() {
			out = append(out, toProviderConfigResponse(c, submitters[c.Provider], descriptions[c.Provider]))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FetchPriority != out[j].FetchPriority {
			return out[i].FetchPriority < out[j].FetchPriority
		}
		return out[i].Provider < out[j].Provider
	})
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// HandleUpdateProvider updates a provider's config row.
func (h *AdminMarkerProvidersHandler) HandleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker providers are not configured")
		return
	}
	provider, err := decodedURLParam(r, "provider")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid provider ID")
		return
	}
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
		h.logger.ErrorContext(r.Context(), "admin markers: update provider config failed", "provider", provider, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update provider")
		return
	}
	if h.EventBus != nil {
		_ = h.EventBus.Publish(r.Context(), cache.ChannelAdmin, cache.Event{
			Type:    cache.EventMarkerProviderConfigChanged,
			Payload: provider,
		})
	}
	writeJSON(w, http.StatusOK, toProviderConfigResponse(existing, h.submitterIDs()[provider], h.providerDescriptions()[provider]))
}

// HandleValidateProvider validates the provider's configured key and returns stats.
func (h *AdminMarkerProvidersHandler) HandleValidateProvider(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Registry == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker providers are not configured")
		return
	}
	provider, err := decodedURLParam(r, "provider")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid provider ID")
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "stats": toMarkerUserStatsResponse(stats)})
}

func toProviderConfigResponse(c markers.ProviderConfig, isSubmitter bool, desc markers.ProviderDescriptor) providerConfigResponse {
	return providerConfigResponse{
		Provider:                c.Provider,
		DisplayName:             desc.DisplayName,
		SourceType:              desc.SourceType,
		PluginID:                desc.PluginID,
		PluginInstallationID:    desc.PluginInstallationID,
		CapabilityID:            desc.CapabilityID,
		IsSubmitter:             isSubmitter,
		FetchEnabled:            c.FetchEnabled,
		FetchPriority:           c.FetchPriority,
		ContributeEnabled:       c.ContributeEnabled,
		ContributeAutoLocal:     c.ContributeAutoLocal,
		ContributeMinConfidence: c.ContributeMinConfidence,
	}
}

func toMarkerUserStatsResponse(s markers.UserStats) markerUserStatsResponse {
	return markerUserStatsResponse{
		Total:          s.Total,
		Accepted:       s.Accepted,
		Pending:        s.Pending,
		Rejected:       s.Rejected,
		AcceptanceRate: s.AcceptanceRate,
		CurrentStreak:  s.CurrentStreak,
		BestStreak:     s.BestStreak,
	}
}

package handlers

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

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
	// FetchMinConfidence and DefaultContributeEnabled are newer than the
	// frozen v1 response, so only v2 emits them (json:"-").
	FetchMinConfidence       float64 `json:"-"`
	DefaultContributeEnabled bool    `json:"-"`
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
	out, err := h.ListMarkerProviders(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

type MarkerProviderConfigView = providerConfigResponse
type MarkerUserStatsView = markerUserStatsResponse

func (h *AdminMarkerProvidersHandler) ListMarkerProviders(ctx context.Context) ([]MarkerProviderConfigView, error) {
	if h == nil || h.Config == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Marker providers are not configured")
	}
	// The stored rows, not this node's cache: a node that missed a sharing
	// opt-out must not show the admin sharing still on.
	configs, err := h.Config.Load(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "admin markers: load provider config failed", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load marker providers")
	}
	submitters := h.submitterIDs()
	descriptions := h.providerDescriptions()
	out := []providerConfigResponse{}
	if h.Registry != nil {
		for _, provider := range h.Registry.Providers() {
			c, ok := configs[provider.ID()]
			if !ok {
				continue
			}
			out = append(out, toProviderConfigResponse(c, submitters[c.Provider], descriptions[c.Provider]))
		}
	} else {
		for _, c := range configs {
			out = append(out, toProviderConfigResponse(c, submitters[c.Provider], descriptions[c.Provider]))
		}
	}
	slices.SortFunc(out, func(a, b providerConfigResponse) int {
		if c := cmp.Compare(a.FetchPriority, b.FetchPriority); c != 0 {
			return c
		}
		return cmp.Compare(a.Provider, b.Provider)
	})
	return out, nil
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
	if _, ok := h.Config.Get(provider); !ok {
		writeError(w, http.StatusNotFound, "not_found", "Unknown marker provider")
		return
	}
	var body MarkerProviderUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	out, err := h.updateMarkerProvider(r.Context(), provider, body)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	out, err := h.ValidateMarkerProvider(r.Context(), provider)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if !out.Valid {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": out.Error})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "stats": out.Stats})
}

func toProviderConfigResponse(c markers.ProviderConfig, isSubmitter bool, desc markers.ProviderDescriptor) providerConfigResponse {
	return providerConfigResponse{
		Provider:                 c.Provider,
		DisplayName:              desc.DisplayName,
		SourceType:               desc.SourceType,
		PluginID:                 desc.PluginID,
		PluginInstallationID:     desc.PluginInstallationID,
		CapabilityID:             desc.CapabilityID,
		IsSubmitter:              isSubmitter,
		FetchEnabled:             c.FetchEnabled,
		FetchPriority:            c.FetchPriority,
		ContributeEnabled:        c.ContributeEnabled,
		ContributeAutoLocal:      c.ContributeAutoLocal,
		ContributeMinConfidence:  c.ContributeMinConfidence,
		FetchMinConfidence:       c.FetchMinConfidence,
		DefaultContributeEnabled: desc.DefaultContributeEnabled,
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

type MarkerProviderUpdate struct {
	FetchEnabled            *bool    `json:"fetch_enabled"`
	FetchPriority           *int     `json:"fetch_priority"`
	ContributeEnabled       *bool    `json:"contribute_enabled"`
	ContributeAutoLocal     *bool    `json:"contribute_auto_local"`
	ContributeMinConfidence *float64 `json:"contribute_min_confidence"`
	// FetchMinConfidence is set through v2 only; the frozen v1 body cannot
	// carry it (json:"-").
	FetchMinConfidence *float64 `json:"-"`
}

func (h *AdminMarkerProvidersHandler) UpdateMarkerProvider(ctx context.Context, provider string, body MarkerProviderUpdate) (MarkerProviderConfigView, error) {
	if h == nil || h.Config == nil {
		return MarkerProviderConfigView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Marker providers are not configured")
	}
	if _, ok := h.Config.Get(provider); !ok {
		return MarkerProviderConfigView{}, errUnknownMarkerProvider()
	}
	return h.updateMarkerProvider(ctx, provider, body)
}

func errUnknownMarkerProvider() error {
	return apiError(http.StatusNotFound, "not_found", "Unknown marker provider")
}

// updateMarkerProvider writes only the fields the body sets. The stored row,
// not this node's cache, supplies the rest, so a partial edit cannot undo a
// sharing opt-out this node has not seen yet.
func (h *AdminMarkerProvidersHandler) updateMarkerProvider(ctx context.Context, provider string, body MarkerProviderUpdate) (MarkerProviderConfigView, error) {
	if v := body.FetchMinConfidence; v != nil && (*v < 0 || *v > 1) {
		return MarkerProviderConfigView{}, apiError(http.StatusBadRequest, "bad_request", "fetch_min_confidence must be between 0 and 1")
	}
	if v := body.ContributeMinConfidence; v != nil && (*v < 0 || *v > 1) {
		return MarkerProviderConfigView{}, apiError(http.StatusBadRequest, "bad_request", "contribute_min_confidence must be between 0 and 1")
	}
	updated, ok, err := h.Config.Patch(ctx, provider, markers.ProviderConfigPatch{
		FetchEnabled:            body.FetchEnabled,
		FetchPriority:           body.FetchPriority,
		FetchMinConfidence:      body.FetchMinConfidence,
		ContributeEnabled:       body.ContributeEnabled,
		ContributeAutoLocal:     body.ContributeAutoLocal,
		ContributeMinConfidence: body.ContributeMinConfidence,
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "admin markers: update provider config failed", "provider", provider, "error", err)
		return MarkerProviderConfigView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update provider")
	}
	if !ok {
		return MarkerProviderConfigView{}, errUnknownMarkerProvider()
	}
	if h.EventBus != nil {
		_ = h.EventBus.Publish(ctx, cache.ChannelAdmin, cache.Event{
			Type:    cache.EventMarkerProviderConfigChanged,
			Payload: provider,
		})
	}
	return toProviderConfigResponse(updated, h.submitterIDs()[provider], h.providerDescriptions()[provider]), nil
}

type MarkerProviderValidationView struct {
	Valid bool                 `json:"valid"`
	Error string               `json:"error,omitempty"`
	Stats *MarkerUserStatsView `json:"stats,omitempty"`
}

func (h *AdminMarkerProvidersHandler) ValidateMarkerProvider(ctx context.Context, provider string) (MarkerProviderValidationView, error) {
	if h == nil || h.Registry == nil {
		return MarkerProviderValidationView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Marker providers are not configured")
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
		return MarkerProviderValidationView{}, apiError(http.StatusBadRequest, "bad_request", "Provider does not support contribution")
	}
	stats, err := submitter.FetchUserStats(ctx)
	if err != nil {
		return MarkerProviderValidationView{Error: err.Error()}, nil //nolint:nilerr // a rejected credential is the validation result (Valid=false), not a request failure
	}
	return MarkerProviderValidationView{Valid: true, Stats: new(toMarkerUserStatsResponse(stats))}, nil
}

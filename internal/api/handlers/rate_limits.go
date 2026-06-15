package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// RateLimitHandler handles rate limit config admin endpoints.
type RateLimitHandler struct {
	store         ratelimit.SettingsStore
	mw            *ratelimit.Middleware
	eventBus      cache.EventBus
	restartStatus *ServerRestartStatusTracker
}

// NewRateLimitHandler creates a new RateLimitHandler.
func NewRateLimitHandler(store ratelimit.SettingsStore, mw *ratelimit.Middleware, eventBus cache.EventBus, restartStatus *ServerRestartStatusTracker) *RateLimitHandler {
	return &RateLimitHandler{store: store, mw: mw, eventBus: eventBus, restartStatus: restartStatus}
}

type rateLimitConfigResponse struct {
	Enabled            bool                                  `json:"enabled"`
	Backend            string                                `json:"backend"`
	GlobalReqPerSecond float64                               `json:"global_requests_per_second"`
	Tiers              map[string]tierConfigResponse         `json:"tiers"`
	IPReqPerSecond     float64                               `json:"ip_requests_per_second"`
	IPReqPerMinute     float64                               `json:"ip_requests_per_minute"`
	IPBurst            int                                   `json:"ip_burst"`
	AuthEndpoints      map[string]authEndpointConfigResponse `json:"auth_endpoints"`
	// Active reports whether a limiter is running in this process. The
	// limiter is constructed at startup, so config saved while it is absent
	// (or a backend change while it is running) needs a restart to apply.
	Active bool `json:"active"`
	// ActiveBackend is the backend the running limiter actually uses, which
	// can differ from Backend until the server restarts.
	ActiveBackend string `json:"active_backend,omitempty"`
}

type tierConfigResponse struct {
	RequestsPerSecond float64 `json:"requests_per_second"`
	RequestsPerMinute float64 `json:"requests_per_minute"`
	Burst             int     `json:"burst"`
}

type authEndpointConfigResponse struct {
	RequestsPerMinute float64 `json:"requests_per_minute"`
	Burst             int     `json:"burst"`
}

// HandleGetConfig handles GET /admin/rate-limits/config.
func (h *RateLimitHandler) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := ratelimit.LoadConfig(r.Context(), h.store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load rate limit config")
		return
	}

	backend, _ := h.store.Get(r.Context(), "ratelimit.backend")
	if backend == "" {
		backend = "memory"
	}

	resp := rateLimitConfigResponse{
		Enabled:            cfg.Enabled,
		Backend:            backend,
		GlobalReqPerSecond: cfg.GlobalReqPerSecond,
		Tiers:              make(map[string]tierConfigResponse),
		IPReqPerSecond:     cfg.IPReqPerSecond,
		IPReqPerMinute:     cfg.IPReqPerMinute,
		IPBurst:            cfg.IPBurst,
		AuthEndpoints:      make(map[string]authEndpointConfigResponse),
		Active:             h.mw != nil,
	}
	if h.mw != nil {
		resp.ActiveBackend = h.mw.ActiveBackend()
	}
	for name, tier := range cfg.Tiers {
		resp.Tiers[name] = tierConfigResponse{
			RequestsPerSecond: tier.RequestsPerSecond,
			RequestsPerMinute: tier.RequestsPerMinute,
			Burst:             tier.Burst,
		}
	}
	for name, ep := range cfg.AuthEndpoints {
		resp.AuthEndpoints[name] = authEndpointConfigResponse{
			RequestsPerMinute: ep.RequestsPerMinute,
			Burst:             ep.Burst,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleUpdateConfig handles PUT /admin/rate-limits/config.
func (h *RateLimitHandler) HandleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req rateLimitConfigResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Load existing config so we can preserve fields not included in the request.
	existing, err := ratelimit.LoadConfig(r.Context(), h.store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load existing config")
		return
	}

	cfg := ratelimit.Config{
		Enabled:            req.Enabled,
		GlobalReqPerSecond: req.GlobalReqPerSecond,
		Tiers:              make(map[string]ratelimit.TierConfig),
		IPReqPerSecond:     req.IPReqPerSecond,
		IPReqPerMinute:     req.IPReqPerMinute,
		IPBurst:            req.IPBurst,
		AuthEndpoints:      make(map[string]ratelimit.AuthEndpointConfig),
	}

	// Preserve IP settings if not provided (zero values mean omitted from request).
	if cfg.IPReqPerSecond == 0 {
		cfg.IPReqPerSecond = existing.IPReqPerSecond
	}
	if cfg.IPReqPerMinute == 0 {
		cfg.IPReqPerMinute = existing.IPReqPerMinute
	}
	if cfg.IPBurst == 0 {
		cfg.IPBurst = existing.IPBurst
	}

	for name, tier := range req.Tiers {
		cfg.Tiers[name] = ratelimit.TierConfig{
			RequestsPerSecond: tier.RequestsPerSecond,
			RequestsPerMinute: tier.RequestsPerMinute,
			Burst:             tier.Burst,
		}
	}

	// Preserve existing auth endpoint settings if not provided in request.
	for name, ep := range existing.AuthEndpoints {
		cfg.AuthEndpoints[name] = ep
	}
	for name, ep := range req.AuthEndpoints {
		cfg.AuthEndpoints[name] = ratelimit.AuthEndpointConfig{
			RequestsPerMinute: ep.RequestsPerMinute,
			Burst:             ep.Burst,
		}
	}

	if err := ratelimit.SaveConfig(r.Context(), h.store, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save rate limit config")
		return
	}

	// Save backend setting (infrastructure-level, requires restart)
	if req.Backend == "memory" || req.Backend == "redis" {
		currentBackend, _ := h.store.Get(r.Context(), "ratelimit.backend")
		if currentBackend == "" {
			currentBackend = "memory"
		}
		if err := h.store.Set(r.Context(), "ratelimit.backend", req.Backend); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save backend setting")
			return
		}
		if currentBackend != req.Backend {
			h.restartStatus.MarkRequired("ratelimit_backend")
		}
	}

	// The limiter is constructed at startup, so enabling while it is absent
	// or switching backend while it runs only takes effect after a restart.
	// Everything else hot-reloads below.
	restartRequired := false
	if h.mw == nil {
		restartRequired = req.Enabled
	} else {
		if (req.Backend == "memory" || req.Backend == "redis") && req.Backend != h.mw.ActiveBackend() {
			restartRequired = true
		}
		// Hot-reload: apply new config immediately on this instance
		if err := h.mw.Reload(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Config saved but reload failed")
			return
		}
	}

	// Publish for multi-instance reload (if EventBus is available/backed by Redis)
	if h.eventBus != nil {
		_ = h.eventBus.Publish(r.Context(), cache.ChannelAdmin, cache.Event{
			Type: cache.EventSettingsChanged,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "restart_required": restartRequired})
}

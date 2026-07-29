package handlers

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// RateLimitHandler handles rate limit config admin endpoints.
type RateLimitHandler struct {
	store                   ratelimit.SettingsStore
	mw                      *ratelimit.Middleware
	eventBus                cache.EventBus
	restartStatus           *ServerRestartStatusTracker
	redisBootstrapAvailable bool
}

// NewRateLimitHandler creates a new RateLimitHandler.
func NewRateLimitHandler(store ratelimit.SettingsStore, mw *ratelimit.Middleware, eventBus cache.EventBus, restartStatus *ServerRestartStatusTracker, redisBootstrapAvailable ...bool) *RateLimitHandler {
	return &RateLimitHandler{
		store: store, mw: mw, eventBus: eventBus, restartStatus: restartStatus,
		redisBootstrapAvailable: len(redisBootstrapAvailable) > 0 && redisBootstrapAvailable[0],
	}
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

type rateLimitConfigRequest struct {
	Enabled            *bool                                `json:"enabled"`
	Backend            string                               `json:"backend"`
	GlobalReqPerSecond *float64                             `json:"global_requests_per_second"`
	Tiers              map[string]tierConfigRequest         `json:"tiers"`
	IPReqPerSecond     *float64                             `json:"ip_requests_per_second"`
	IPReqPerMinute     *float64                             `json:"ip_requests_per_minute"`
	IPBurst            *int                                 `json:"ip_burst"`
	AuthEndpoints      map[string]authEndpointConfigRequest `json:"auth_endpoints"`
}

type tierConfigRequest struct {
	RequestsPerSecond *float64 `json:"requests_per_second"`
	RequestsPerMinute *float64 `json:"requests_per_minute"`
	Burst             *int     `json:"burst"`
}

type authEndpointConfigRequest struct {
	RequestsPerMinute *float64 `json:"requests_per_minute"`
	Burst             *int     `json:"burst"`
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
	var req rateLimitConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updater, ok := h.store.(serverSettingsAtomicUpdater)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store does not support atomic updates")
		return
	}

	var (
		changed      bool
		requestErr   error
		requestError = "invalid_rate_limit_config"
	)
	err := updater.UpdateAtomic(r.Context(), func(current map[string]string) (map[string]string, error) {
		existing := ratelimit.ConfigFromSettings(current)
		merged, err := mergeRateLimitConfig(existing, req)
		if err != nil {
			requestErr = err
			return nil, err
		}

		currentBackend := strings.TrimSpace(strings.ToLower(current["ratelimit.backend"]))
		if currentBackend == "" {
			currentBackend = "memory"
		}
		backend := strings.TrimSpace(strings.ToLower(req.Backend))
		if backend == "" {
			backend = currentBackend
		}
		if backend != "memory" && backend != "redis" {
			requestErr = fmt.Errorf("backend must be memory or redis")
			return nil, requestErr
		}
		if backend == "redis" && !redisConfiguredSettings(current, h.redisBootstrapAvailable) {
			requestError = "redis_not_configured"
			requestErr = fmt.Errorf("configure a Redis URL, or start the server with a valid Sentinel deployment, before selecting the Redis rate-limit backend")
			return nil, requestErr
		}

		values := ratelimit.ConfigSettings(merged)
		values["ratelimit.backend"] = backend
		currentValues := ratelimit.ConfigSettings(existing)
		currentValues["ratelimit.backend"] = currentBackend
		changed = !maps.Equal(values, currentValues)
		if !changed {
			return nil, nil
		}
		return values, nil
	})
	if requestErr != nil {
		writeError(w, http.StatusBadRequest, requestError, requestErr.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save rate limit config")
		return
	}
	if !changed {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "restart_required": false})
		return
	}

	// Another process may have committed a newer settings mutation after this
	// request released the mutation lock. Base post-commit behavior on a fresh
	// snapshot so reordered requests converge on the latest durable state.
	latest, err := h.store.GetAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Config saved but latest settings could not be loaded")
		return
	}
	latestConfig := ratelimit.ConfigFromSettings(latest)
	latestBackend := strings.TrimSpace(strings.ToLower(latest["ratelimit.backend"]))
	if latestBackend == "" {
		latestBackend = "memory"
	}

	// The limiter is constructed at startup, so enabling while it is absent
	// or switching backend while it runs only takes effect after a restart.
	// Everything else hot-reloads below.
	restartRequired := false
	if h.mw == nil {
		restartRequired = latestConfig.Enabled
	} else {
		if latestBackend != h.mw.ActiveBackend() {
			restartRequired = true
		}
		// Reload reads the store again rather than applying the request-local
		// merge, ensuring the middleware receives the latest committed config.
		if err := h.mw.Reload(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Config saved but reload failed")
			return
		}
	}
	if restartRequired {
		h.restartStatus.MarkRequired("ratelimit_backend")
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

func mergeRateLimitConfig(existing ratelimit.Config, req rateLimitConfigRequest) (ratelimit.Config, error) {
	cfg := existing
	cfg.Tiers = make(map[string]ratelimit.TierConfig, len(existing.Tiers))
	for name, tier := range existing.Tiers {
		cfg.Tiers[name] = tier
	}
	cfg.AuthEndpoints = make(map[string]ratelimit.AuthEndpointConfig, len(existing.AuthEndpoints))
	for name, endpoint := range existing.AuthEndpoints {
		cfg.AuthEndpoints[name] = endpoint
	}

	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.GlobalReqPerSecond != nil {
		cfg.GlobalReqPerSecond = *req.GlobalReqPerSecond
	}
	if req.IPReqPerSecond != nil {
		cfg.IPReqPerSecond = *req.IPReqPerSecond
	}
	if req.IPReqPerMinute != nil {
		cfg.IPReqPerMinute = *req.IPReqPerMinute
	}
	if req.IPBurst != nil {
		cfg.IPBurst = *req.IPBurst
	}

	for name, update := range req.Tiers {
		tier, ok := cfg.Tiers[name]
		if !ok {
			return ratelimit.Config{}, fmt.Errorf("unknown API-key tier %q", name)
		}
		if update.RequestsPerSecond != nil {
			tier.RequestsPerSecond = *update.RequestsPerSecond
		}
		if update.RequestsPerMinute != nil {
			tier.RequestsPerMinute = *update.RequestsPerMinute
		}
		if update.Burst != nil {
			tier.Burst = *update.Burst
		}
		cfg.Tiers[name] = tier
	}

	for name, update := range req.AuthEndpoints {
		endpoint, ok := cfg.AuthEndpoints[name]
		if !ok {
			return ratelimit.Config{}, fmt.Errorf("unknown auth endpoint %q", name)
		}
		if update.RequestsPerMinute != nil {
			endpoint.RequestsPerMinute = *update.RequestsPerMinute
		}
		if update.Burst != nil {
			endpoint.Burst = *update.Burst
		}
		cfg.AuthEndpoints[name] = endpoint
	}

	if err := validateRateLimitConfig(cfg); err != nil {
		return ratelimit.Config{}, err
	}
	return cfg, nil
}

func validateRateLimitConfig(cfg ratelimit.Config) error {
	if err := boundedRate("global_requests_per_second", cfg.GlobalReqPerSecond, ratelimit.MaxGlobalRequestsPerSecond); err != nil {
		return err
	}
	if err := boundedRate("ip_requests_per_second", cfg.IPReqPerSecond, ratelimit.MaxRequestsPerWindow); err != nil {
		return err
	}
	if err := boundedRate("ip_requests_per_minute", cfg.IPReqPerMinute, ratelimit.MaxRequestsPerWindow); err != nil {
		return err
	}
	if err := boundedBurst("ip_burst", cfg.IPBurst); err != nil {
		return err
	}
	for name, tier := range cfg.Tiers {
		if err := boundedRate("tier."+name+".requests_per_second", tier.RequestsPerSecond, ratelimit.MaxRequestsPerWindow); err != nil {
			return err
		}
		if err := boundedRate("tier."+name+".requests_per_minute", tier.RequestsPerMinute, ratelimit.MaxRequestsPerWindow); err != nil {
			return err
		}
		if err := boundedBurst("tier."+name+".burst", tier.Burst); err != nil {
			return err
		}
	}
	for name, endpoint := range cfg.AuthEndpoints {
		if err := boundedRate("auth."+name+".requests_per_minute", endpoint.RequestsPerMinute, ratelimit.MaxRequestsPerWindow); err != nil {
			return err
		}
		if err := boundedBurst("auth."+name+".burst", endpoint.Burst); err != nil {
			return err
		}
	}
	return nil
}

func boundedRate(name string, value, maxValue float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > maxValue {
		return fmt.Errorf("%s must be a finite number greater than zero and no greater than %g", name, maxValue)
	}
	return nil
}

func boundedBurst(name string, value int) error {
	if value <= 0 || value > ratelimit.MaxBurst {
		return fmt.Errorf("%s must be an integer between 1 and %d", name, ratelimit.MaxBurst)
	}
	return nil
}

func redisConfiguredSettings(values map[string]string, redisBootstrapAvailable bool) bool {
	// Sentinel addresses are bootstrap-only and intentionally have no flat
	// server_settings representation (see config.LoadFromDB). A usable Sentinel
	// deployment or REDIS_URL override is therefore captured by
	// redisBootstrapAvailable from startup config and takes precedence over any
	// stale persisted redis.url row.
	if redisBootstrapAvailable {
		return true
	}

	// redis.url is the only Redis transport that can become usable from a
	// persisted Admin setting before the next restart.
	redisURL := values["redis.url"]
	if redisURL == "" {
		return false
	}
	normalized, err := config.NormalizeRedisURL(redisURL)
	// The startup loader consumes the persisted value verbatim. Require its
	// stored representation to already be canonical so a value accepted here
	// cannot fail after restart.
	return err == nil && normalized == redisURL
}

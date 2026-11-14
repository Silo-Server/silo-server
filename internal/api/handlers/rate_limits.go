package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

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

type rateLimitConfigResponse = AdminRateLimitConfigView

type AdminRateLimitConfigView struct {
	Enabled            bool                                  `json:"enabled"`
	Backend            string                                `json:"backend"`
	GlobalReqPerSecond float64                               `json:"global_requests_per_second"`
	Tiers              map[string]tierConfigResponse         `json:"tiers"`
	IPReqPerSecond     float64                               `json:"ip_requests_per_second"`
	IPReqPerMinute     float64                               `json:"ip_requests_per_minute"`
	IPBurst            int                                   `json:"ip_burst"`
	AuthEndpoints      map[string]authEndpointConfigResponse `json:"auth_endpoints"`
	// SessionQuota is served on /api/v2 only; the frozen v1 handlers drop it.
	SessionQuota *AdminRateLimitSessionQuotaView `json:"session_quota,omitempty"`
	// Active reports whether a limiter is running in this process. The
	// limiter is constructed at startup, so config saved while it is absent
	// (or a backend change while it is running) needs a restart to apply.
	Active bool `json:"active"`
	// ActiveBackend is the backend the running limiter actually uses, which
	// can differ from Backend until the server restarts.
	ActiveBackend string `json:"active_backend,omitempty"`
	// RedisAvailable reports whether the Redis backend can be selected at all,
	// using the same rule the save path enforces. REDIS_URL deployments have
	// no persisted redis.url row, so admins cannot derive this client-side.
	RedisAvailable bool `json:"redis_available"`
}

// AdminRateLimitSessionQuotaView is the hourly quota each sign-in session gets.
type AdminRateLimitSessionQuotaView struct {
	Mode            string  `json:"mode"`
	RequestsPerHour float64 `json:"requests_per_hour"`
	Burst           int     `json:"burst"`
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

type rateLimitConfigRequest = AdminRateLimitUpdate

type AdminRateLimitUpdate struct {
	Enabled            *bool                                `json:"enabled,omitempty"`
	Backend            string                               `json:"backend,omitempty"`
	GlobalReqPerSecond *float64                             `json:"global_requests_per_second,omitempty"`
	Tiers              map[string]tierConfigRequest         `json:"tiers,omitempty"`
	IPReqPerSecond     *float64                             `json:"ip_requests_per_second,omitempty"`
	IPReqPerMinute     *float64                             `json:"ip_requests_per_minute,omitempty"`
	IPBurst            *int                                 `json:"ip_burst,omitempty"`
	AuthEndpoints      map[string]authEndpointConfigRequest `json:"auth_endpoints,omitempty"`
	SessionQuota       *AdminRateLimitSessionQuotaUpdate    `json:"session_quota,omitempty"`
}

// AdminRateLimitSessionQuotaUpdate merges into the session quota; omitted
// fields keep their values.
type AdminRateLimitSessionQuotaUpdate struct {
	Mode            *string  `json:"mode,omitempty"`
	RequestsPerHour *float64 `json:"requests_per_hour,omitempty"`
	Burst           *int     `json:"burst,omitempty"`
}

type tierConfigRequest = AdminRateLimitTierUpdate

type AdminRateLimitTierUpdate struct {
	RequestsPerSecond *float64 `json:"requests_per_second,omitempty"`
	RequestsPerMinute *float64 `json:"requests_per_minute,omitempty"`
	Burst             *int     `json:"burst,omitempty"`
}

type authEndpointConfigRequest = AdminRateLimitAuthEndpointUpdate

type AdminRateLimitAuthEndpointUpdate struct {
	RequestsPerMinute *float64 `json:"requests_per_minute,omitempty"`
	Burst             *int     `json:"burst,omitempty"`
}

// ReadAdminRateLimitConfig reads desired settings once and adds process observations.
func (h *RateLimitHandler) ReadAdminRateLimitConfig(ctx context.Context) (AdminRateLimitConfigView, error) {
	// One read serves the rate values, the stored backend, and the
	// Redis-availability bit, so no field of the response can straddle two
	// snapshots of the settings table.
	values, err := h.store.GetAll(ctx)
	if err != nil {
		return rateLimitConfigResponse{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load rate limit config")
	}
	return h.adminRateLimitConfigView(values), nil
}

func (h *RateLimitHandler) adminRateLimitConfigView(values map[string]string) AdminRateLimitConfigView {
	cfg := ratelimit.ConfigFromSettings(values)

	backend := values["ratelimit.backend"]
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
		SessionQuota: &AdminRateLimitSessionQuotaView{
			Mode: cfg.Session.Mode, RequestsPerHour: cfg.Session.RequestsPerHour, Burst: cfg.Session.Burst,
		},
		Active:         h.mw != nil,
		RedisAvailable: redisConfiguredSettings(values, h.redisBootstrapAvailable),
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

	return resp
}

type AdminRateLimitUpdateResult struct {
	Status          string `json:"status"`
	RestartRequired bool   `json:"restart_required"`
}

// UpdateAdminRateLimitConfig checks the guard against the locked durable state.
// Post-commit reload and event publication retain their existing best-effort scope.
func (h *RateLimitHandler) UpdateAdminRateLimitConfig(ctx context.Context, req AdminRateLimitUpdate, guard func(AdminRateLimitConfigView) error) (AdminRateLimitUpdateResult, error) {
	updater, ok := h.store.(serverSettingsAtomicUpdater)
	if !ok {
		return AdminRateLimitUpdateResult{}, apiError(http.StatusInternalServerError, "internal_error", "Settings store does not support atomic updates")
	}

	var guardErr error
	var (
		changed      bool
		requestErr   error
		requestError = "invalid_rate_limit_config"
	)
	err := updater.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		if guard != nil {
			guardErr = guard(h.adminRateLimitConfigView(current))
			if guardErr != nil {
				return nil, guardErr
			}
		}
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
			requestErr = fmt.Errorf("configure a Redis URL before selecting the Redis rate-limit backend")
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
	if guardErr != nil {
		return AdminRateLimitUpdateResult{}, guardErr
	}
	if requestErr != nil {
		return AdminRateLimitUpdateResult{}, apiError(http.StatusBadRequest, requestError, requestErr.Error())
	}
	if err != nil {
		return AdminRateLimitUpdateResult{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to save rate limit config")
	}
	if !changed {
		return AdminRateLimitUpdateResult{Status: "ok"}, nil
	}
	restartRequired, err := h.rateLimitsChanged(ctx)
	if err != nil {
		return AdminRateLimitUpdateResult{}, err
	}
	return AdminRateLimitUpdateResult{Status: "ok", RestartRequired: restartRequired}, nil
}

// rateLimitsChanged applies a committed rate-limit change: it reloads the
// limiter, flags a restart the change needs, and tells other servers.
func (h *RateLimitHandler) rateLimitsChanged(ctx context.Context) (bool, error) {
	// Another process may have committed a newer settings mutation after this
	// request released the mutation lock. Base post-commit behavior on a fresh
	// snapshot so reordered requests converge on the latest durable state.
	latest, err := h.store.GetAll(ctx)
	if err != nil {
		return false, apiError(http.StatusInternalServerError, "internal_error", "Config saved but latest settings could not be loaded")
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
		if err := h.mw.Reload(ctx); err != nil {
			return false, apiError(http.StatusInternalServerError, "internal_error", "Config saved but reload failed")
		}
	}
	if restartRequired {
		h.restartStatus.MarkRequired("ratelimit_backend")
	}

	// Publish for multi-instance reload (if EventBus is available/backed by Redis)
	if h.eventBus != nil {
		_ = h.eventBus.Publish(ctx, cache.ChannelAdmin, cache.Event{
			Type: cache.EventSettingsChanged,
		})
	}

	return restartRequired, nil
}

// serverSettingsTxUpdater writes settings in the caller's transaction.
type serverSettingsTxUpdater interface {
	UpdateAtomicWithTx(ctx context.Context, tx pgx.Tx, update func(current map[string]string, tx pgx.Tx) (map[string]string, error)) error
}

// ApplyRateLimitSettingsInTx writes restored rate-limit settings (ratelimit.*)
// in the caller's transaction with the checks UpdateAdminRateLimitConfig
// applies: the limits as a whole must validate, the backend must be memory
// or redis, and redis needs a configured Redis. check sees the settings as
// stored under the lock first. The returned func runs once the transaction
// commits.
func (h *RateLimitHandler) ApplyRateLimitSettingsInTx(ctx context.Context, tx pgx.Tx, values map[string]string, check func(map[string]string) error) (func(context.Context), error) {
	updater, ok := h.store.(serverSettingsTxUpdater)
	if !ok || tx == nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Settings store does not support caller transactions")
	}
	err := updater.UpdateAtomicWithTx(ctx, tx, func(current map[string]string, _ pgx.Tx) (map[string]string, error) {
		if check != nil {
			if err := check(current); err != nil {
				return nil, err
			}
		}
		next := maps.Clone(current)
		maps.Copy(next, values)
		cfg := ratelimit.ConfigFromSettings(next)
		if err := validateRateLimitConfig(cfg); err != nil {
			return nil, apiError(http.StatusBadRequest, "invalid_rate_limit_config", err.Error())
		}
		backend := strings.TrimSpace(strings.ToLower(next["ratelimit.backend"]))
		if backend == "" {
			backend = "memory"
		}
		if backend != "memory" && backend != "redis" {
			return nil, apiError(http.StatusBadRequest, "invalid_rate_limit_config", "backend must be memory or redis")
		}
		if backend == "redis" && !redisConfiguredSettings(current, h.redisBootstrapAvailable) {
			return nil, apiError(http.StatusBadRequest, "redis_not_configured", "configure a Redis URL before selecting the Redis rate-limit backend")
		}
		out := ratelimit.ConfigSettings(cfg)
		out["ratelimit.backend"] = backend
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) {
		if _, err := h.rateLimitsChanged(ctx); err != nil {
			slog.WarnContext(ctx, "applying restored rate limits failed", "component", "ratelimit", "error", err)
		}
	}, nil
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
	if q := req.SessionQuota; q != nil {
		if q.Mode != nil {
			cfg.Session.Mode = strings.TrimSpace(strings.ToLower(*q.Mode))
		}
		if q.RequestsPerHour != nil {
			cfg.Session.RequestsPerHour = *q.RequestsPerHour
		}
		if q.Burst != nil {
			cfg.Session.Burst = *q.Burst
		}
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
	if !ratelimit.ValidSessionQuotaMode(cfg.Session.Mode) {
		return errors.New("session_quota.mode must be off, log or enforce")
	}
	if err := boundedRate("session_quota.requests_per_hour", cfg.Session.RequestsPerHour, ratelimit.MaxRequestsPerWindow); err != nil {
		return err
	}
	if err := boundedBurst("session_quota.burst", cfg.Session.Burst); err != nil {
		return err
	}
	if !ratelimit.SessionQuotaRefillsInTime(cfg.Session) {
		return errors.New("session_quota.burst must refill within a week: at most 168 times requests_per_hour")
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
	// A usable REDIS_URL override is captured by redisBootstrapAvailable from
	// startup config and takes precedence over any stale persisted redis.url
	// row.
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

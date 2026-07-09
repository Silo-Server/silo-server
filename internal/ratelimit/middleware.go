package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

// Middleware manages rate limiting config, limiters, and the HTTP handler.
type Middleware struct {
	mu       sync.RWMutex
	cfg      Config
	perKey   RateLimiter
	global   RateLimiter
	store    SettingsStore
	isMemory bool
}

// NewMiddleware creates the rate limit middleware.
func NewMiddleware(perKey RateLimiter, global RateLimiter, store SettingsStore, isMemory bool) *Middleware {
	return &Middleware{
		perKey:   perKey,
		global:   global,
		store:    store,
		isMemory: isMemory,
	}
}

// ActiveBackend reports which limiter backend this process is actually
// running, as opposed to the configured backend, which only takes effect
// after a restart.
func (mw *Middleware) ActiveBackend() string {
	if mw.isMemory {
		return "memory"
	}
	return "redis"
}

// Init loads config and seeds defaults. Call once at startup.
func (mw *Middleware) Init(ctx context.Context) error {
	if err := SeedDefaults(ctx, mw.store); err != nil {
		return err
	}
	return mw.Reload(ctx)
}

// Reload re-reads config from server_settings and clears in-memory state.
func (mw *Middleware) Reload(ctx context.Context) error {
	cfg, err := LoadConfig(ctx, mw.store)
	if err != nil {
		return err
	}
	mw.mu.Lock()
	mw.cfg = cfg
	mw.mu.Unlock()

	if mw.isMemory {
		if ml, ok := mw.perKey.(*MemoryLimiter); ok {
			ml.Clear()
		}
		if ml, ok := mw.global.(*MemoryLimiter); ok {
			ml.Clear()
		}
	}

	slog.InfoContext(ctx, "rate limit config reloaded", "component", "ratelimit", "enabled", cfg.Enabled, "global_rps", cfg.GlobalReqPerSecond)
	return nil
}

// Handler returns the chi-compatible middleware handler.
func (mw *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw.mu.RLock()
		cfg := mw.cfg
		mw.mu.RUnlock()

		if !cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		// Check global limiter (per-second only — spec says no per-minute global limit).
		// Setting RequestsPerMinute = RPS*60 makes the per-minute limiter a mathematical
		// no-op: it never triggers before the per-second limiter does.
		globalRate := Rate{
			RequestsPerSecond: cfg.GlobalReqPerSecond,
			RequestsPerMinute: cfg.GlobalReqPerSecond * 60,
			Burst:             int(cfg.GlobalReqPerSecond),
		}
		globalResult := mw.global.Allow(r.Context(), "global", globalRate)
		if !globalResult.Allowed {
			writeRateLimitResponse(w, globalResult)
			return
		}

		// Check per-IP limiter
		if clientIP := clientip.FromContext(r.Context()); clientIP != "" {
			ipRate := Rate{
				RequestsPerSecond: cfg.IPReqPerSecond,
				RequestsPerMinute: cfg.IPReqPerMinute,
				Burst:             cfg.IPBurst,
			}
			ipKey := "ip:" + clientIP
			ipResult := mw.perKey.Allow(r.Context(), ipKey, ipRate)
			if !ipResult.Allowed {
				writeRateLimitResponse(w, ipResult)
				return
			}
		}

		// Check per-key limiter (API key auth only)
		claims := apimw.GetClaims(r.Context())
		if claims != nil && claims.TokenType == auth.TokenTypeAPIKey && claims.APIKeyID != 0 {
			// RateTier is already on the claims — no DB lookup needed
			tier := claims.RateTier
			if tier == "" {
				tier = "standard"
			}
			tierCfg, ok := cfg.Tiers[tier]
			if !ok {
				tierCfg = cfg.Tiers["standard"]
			}

			keyRate := Rate{
				RequestsPerSecond: tierCfg.RequestsPerSecond,
				RequestsPerMinute: tierCfg.RequestsPerMinute,
				Burst:             tierCfg.Burst,
			}
			key := fmt.Sprintf("key:%d", claims.APIKeyID)
			result := mw.perKey.Allow(r.Context(), key, keyRate)

			// Set rate limit headers on every API-key-authenticated response
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
			if result.Remaining >= 0 {
				w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
			}
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))

			if !result.Allowed {
				writeRateLimitResponse(w, result)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

type rateLimitError struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	RetryAfter int    `json:"retry_after"`
}

// AuthEndpointHandler returns middleware for IP-based rate limiting on auth endpoints.
// It checks both the global per-IP limit and the tighter per-endpoint limit.
func (mw *Middleware) AuthEndpointHandler(endpoint string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mw.mu.RLock()
			cfg := mw.cfg
			mw.mu.RUnlock()

			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			clientIP := clientip.FromContext(r.Context())
			if clientIP == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Check global per-IP limit (shared counter with authenticated routes)
			ipRate := Rate{
				RequestsPerSecond: cfg.IPReqPerSecond,
				RequestsPerMinute: cfg.IPReqPerMinute,
				Burst:             cfg.IPBurst,
			}
			ipResult := mw.perKey.Allow(r.Context(), "ip:"+clientIP, ipRate)
			if !ipResult.Allowed {
				writeRateLimitResponse(w, ipResult)
				return
			}

			// Check per-endpoint limit
			epCfg, ok := cfg.AuthEndpoints[endpoint]
			if ok {
				epRate := Rate{
					RequestsPerSecond: epCfg.RequestsPerMinute / 60,
					RequestsPerMinute: epCfg.RequestsPerMinute,
					Burst:             epCfg.Burst,
				}
				epKey := fmt.Sprintf("authip:%s:%s", clientIP, endpoint)
				epResult := mw.perKey.Allow(r.Context(), epKey, epRate)
				if !epResult.Allowed {
					writeRateLimitResponse(w, epResult)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeRateLimitResponse(w http.ResponseWriter, result AllowResult) {
	retrySeconds := int(result.RetryAfter.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
	w.Header().Set("X-RateLimit-Remaining", "0")
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(rateLimitError{
		Error:      "rate_limit_exceeded",
		Message:    fmt.Sprintf("Too many requests. Please retry after %d seconds.", retrySeconds),
		RetryAfter: retrySeconds,
	})
}

package ratelimit

import (
	"context"
	"time"
)

// Rate defines the rate limits for a key.
type Rate struct {
	RequestsPerSecond float64
	RequestsPerMinute float64
	Burst             int // immediate burst allowance (token bucket or Redis second window)
}

// AllowResult contains the result of a rate limit check.
type AllowResult struct {
	Allowed    bool
	RetryAfter time.Duration
	Limit      int
	Remaining  int
	ResetAt    time.Time
}

// RateLimiter checks whether a request is allowed under the given rate.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit Rate) AllowResult
	Close()
}

// TierConfig holds the rate configuration for a tier.
type TierConfig struct {
	RequestsPerSecond float64
	RequestsPerMinute float64
	Burst             int
}

// AuthEndpointConfig holds per-endpoint rate limit settings for auth endpoints.
type AuthEndpointConfig struct {
	RequestsPerMinute float64
	Burst             int
}

// Config holds all runtime rate limit settings loaded from server_settings.
type Config struct {
	Enabled            bool
	GlobalReqPerSecond float64
	Tiers              map[string]TierConfig
	// IP-based rate limiting
	IPReqPerSecond float64
	IPReqPerMinute float64
	IPBurst        int
	// Auth endpoint per-IP limits
	AuthEndpoints map[string]AuthEndpointConfig
}

// DefaultConfig returns the default rate limit configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		GlobalReqPerSecond: 1000,
		Tiers: map[string]TierConfig{
			"standard": {RequestsPerSecond: 20, RequestsPerMinute: 1200, Burst: 20},
			"elevated": {RequestsPerSecond: 100, RequestsPerMinute: 6000, Burst: 100},
		},
		IPReqPerSecond: 120,
		IPReqPerMinute: 6000,
		IPBurst:        120,
		AuthEndpoints: map[string]AuthEndpointConfig{
			"login":         {RequestsPerMinute: 20, Burst: 10},
			"signup":        {RequestsPerMinute: 10, Burst: 6},
			"setup":         {RequestsPerMinute: 10, Burst: 6},
			"device_start":  {RequestsPerMinute: 20, Burst: 10},
			"device_lookup": {RequestsPerMinute: 60, Burst: 20},
			"device_poll":   {RequestsPerMinute: 120, Burst: 30},
			// Public autoscan webhook intake. Generous: arr fires one delivery
			// per imported file, so season packs are legitimate bursts.
			"autoscan_webhook": {RequestsPerMinute: 60, Burst: 30},
		},
	}
}
